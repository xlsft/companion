package bench

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
)

// Utterance is one reference recording.
type Utterance struct {
	Lang      string
	Path      string
	Reference string
	PCM       []int16
}

func (u Utterance) Duration() time.Duration {
	return time.Duration(len(u.PCM)) * time.Second / protocol.SampleRate
}

// LoadCorpus reads dir/corpus.tsv (lang TAB wav path relative to dir TAB
// reference) and keeps at most perLang utterances per language, in file
// order, so runs are repeatable. Audio is never copied into the repo.
func LoadCorpus(dir string, perLang map[string]int, peak float64) ([]Utterance, error) {
	f, err := os.Open(filepath.Join(dir, "corpus.tsv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Utterance
	n := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for line := 1; sc.Scan(); line++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		f := strings.SplitN(sc.Text(), "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("corpus.tsv:%d: want lang, path, reference", line)
		}
		lang := f[0]
		limit, wanted := perLang[lang]
		if !wanted || n[lang] >= limit {
			continue
		}
		pcm, err := ReadWAV(filepath.Join(dir, f[1]), peak)
		if err != nil {
			return nil, fmt.Errorf("corpus.tsv:%d: %w", line, err)
		}
		n[lang]++
		out = append(out, Utterance{Lang: lang, Path: f[1], Reference: f[2], PCM: pcm})
	}
	return out, sc.Err()
}

// Config selects one sidecar run.
type Config struct {
	Sidecar     string
	SidecarArgs []string
	ModelPath   string
	ModelSHA256 string
	Lang        string
	Backend     string // "cpu" or "auto"
	Threads     int
	// Batch utterances run unpaced with partials off (quality, RTF, final
	// decode time); Live ones are paced in real time with partials on
	// (end-of-speech latency, partial latency).
	Batch, Live []Utterance
	// Chunk is the live write size; real clients send 20–100 ms.
	Chunk time.Duration
	Log   func(format string, args ...any)
}

// Result is one model × language × backend row.
type Result struct {
	Model         string      `json:"model"`
	Lang          string      `json:"lang"`
	BackendAsked  string      `json:"backend_requested"`
	BackendInUse  string      `json:"backend_in_use"`
	Fallback      string      `json:"fallback_reason,omitempty"`
	Runtime       string      `json:"runtime_version"`
	LoadMS        float64     `json:"load_ms"`
	PeakRSSMB     float64     `json:"peak_rss_mb"` // 0 when unavailable
	WER           float64     `json:"wer"`
	CER           float64     `json:"cer"`
	Errors        ErrorCounts `json:"error_counts"`
	RTFMean       float64     `json:"rtf_mean"` // decode time / audio time, whole corpus
	RTFP95        float64     `json:"rtf_p95"`
	FinalBatchP50 float64     `json:"final_batch_p50_ms"`
	// End-of-speech to final, live-paced with partials running: the latency
	// the user feels after they stop talking.
	EOSFinalP50 float64 `json:"eos_final_p50_ms"`
	EOSFinalMax float64 `json:"eos_final_max_ms"`
	// First partial after speech onset, and the median gap between partials.
	FirstPartialP50 float64 `json:"first_partial_p50_ms"`
	PartialGapP50   float64 `json:"partial_gap_p50_ms"`
	// NoSpeech counts batch utterances the sidecar VAD rejected (no decode).
	NoSpeech     int     `json:"no_speech"`
	BatchN       int     `json:"batch_n"`
	LiveN        int     `json:"live_n"`
	AudioSeconds float64 `json:"audio_seconds"`
	Error        string  `json:"error,omitempty"`
	// Details are per-utterance rows, kept only on request; they hold
	// transcripts, so they stay out of default output.
	Details []Detail `json:"details,omitempty"`
}

type Detail struct {
	Path       string  `json:"path"`
	AudioMS    float64 `json:"audio_ms"`
	DecodeMS   float64 `json:"decode_ms"`
	Hypothesis string  `json:"hypothesis"`
	Reference  string  `json:"reference"`
	WordEdits  int     `json:"word_edits"`
}

// Run starts the sidecar, measures, and shuts it down.
func Run(ctx context.Context, cfg Config, details bool) (Result, error) {
	if cfg.Chunk <= 0 {
		cfg.Chunk = 100 * time.Millisecond
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	res := Result{Model: filepath.Base(cfg.ModelPath), Lang: cfg.Lang, BackendAsked: cfg.Backend, BatchN: len(cfg.Batch), LiveN: len(cfg.Live)}

	t0 := time.Now()
	c, err := stt.Start(ctx, stt.Config{
		Path: cfg.Sidecar,
		Args: append([]string{"-engine=whisper", "-log-level=warn"}, cfg.SidecarArgs...),
		Init: protocol.InitializeParams{
			ModelPath: cfg.ModelPath, ModelSHA256: cfg.ModelSHA256,
			Language: cfg.Lang, Backend: cfg.Backend, Threads: cfg.Threads,
		},
	})
	res.LoadMS = ms(time.Since(t0))
	if err != nil {
		return res, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.Close(cctx)
	}()
	info := c.Info()
	res.BackendInUse, res.Fallback, res.Runtime = info.BackendInUse, info.FallbackReason, info.RuntimeVersion

	// The first decode pays one-off costs (graph allocation, shader
	// compilation on GPU); a warm-up keeps them out of the percentiles and
	// is reported separately by the load time only.
	if len(cfg.Batch) > 0 {
		if _, err := batchOne(ctx, c, cfg.Lang, cfg.Batch[0]); err != nil {
			return res, fmt.Errorf("warm-up: %w", err)
		}
	}

	var rtf, finalBatch []float64
	for i, u := range cfg.Batch {
		f, err := batchOne(ctx, c, cfg.Lang, u)
		if err != nil {
			return res, err
		}
		if !f.Speech {
			res.NoSpeech++
		}
		ec := Score(u.Reference, f.Text)
		res.Errors.Add(ec)
		res.AudioSeconds += u.Duration().Seconds()
		rtf = append(rtf, f.DecodeTime.Seconds()/u.Duration().Seconds())
		finalBatch = append(finalBatch, ms(f.DecodeTime))
		if details {
			res.Details = append(res.Details, Detail{Path: u.Path, AudioMS: ms(u.Duration()), DecodeMS: ms(f.DecodeTime), Hypothesis: f.Text, Reference: u.Reference, WordEdits: ec.WordEdits})
		}
		if (i+1)%10 == 0 {
			cfg.Log("  batch %d/%d", i+1, len(cfg.Batch))
		}
	}
	res.WER, res.CER = res.Errors.WER(), res.Errors.CER()
	res.RTFMean, res.RTFP95 = mean(rtf), Percentile(rtf, 95)
	res.FinalBatchP50 = Percentile(finalBatch, 50)

	var eos, first, gaps []float64
	for _, u := range cfg.Live {
		l, err := liveOne(ctx, c, cfg.Lang, u, cfg.Chunk)
		if err != nil {
			return res, err
		}
		eos = append(eos, l.eosFinal)
		if l.firstPartial > 0 {
			first = append(first, l.firstPartial)
		}
		gaps = append(gaps, l.gaps...)
		cfg.Log("  live %s: eos→final %.0f ms, %d partials", filepath.Base(u.Path), l.eosFinal, l.partials)
	}
	res.EOSFinalP50, res.EOSFinalMax = Percentile(eos, 50), Percentile(eos, 100)
	res.FirstPartialP50, res.PartialGapP50 = Percentile(first, 50), Percentile(gaps, 50)

	if mb, ok := PeakRSSMB(c.Pid()); ok {
		res.PeakRSSMB = mb
	}
	return res, nil
}

func batchOne(ctx context.Context, c *stt.Client, lang string, u Utterance) (stt.Final, error) {
	s, err := c.StartSession(ctx, stt.WithPartials(false), stt.WithLanguage(lang))
	if err != nil {
		return stt.Final{}, err
	}
	if err := s.Write(u.PCM); err != nil {
		s.Cancel()
		return stt.Final{}, err
	}
	return s.Finish(ctx)
}

type liveStats struct {
	eosFinal     float64
	firstPartial float64
	gaps         []float64
	partials     int
}

// liveOne streams u paced like a microphone and finishes right after the
// last sample: the user stopped talking, so end-of-speech is the moment the
// last chunk was written. Hands-free mode adds the VAD hangover on top; that
// is a setting (default 800 ms), not a model property, so it is not included.
func liveOne(ctx context.Context, c *stt.Client, lang string, u Utterance, chunk time.Duration) (liveStats, error) {
	var st liveStats
	s, err := c.StartSession(ctx, stt.WithPartials(true), stt.WithLanguage(lang))
	if err != nil {
		return st, err
	}
	start := time.Now()
	speechAt := make(chan time.Time, 1)
	partialAt := make(chan []time.Time, 1)
	go func() {
		var once bool
		for sp := range s.Speech() {
			if sp && !once {
				once = true
				speechAt <- time.Now()
			}
		}
	}()
	go func() {
		var ts []time.Time
		for range s.Partials() {
			ts = append(ts, time.Now())
		}
		partialAt <- ts
	}()
	n := int(chunk * protocol.SampleRate / time.Second)
	for i := 0; i < len(u.PCM); i += n {
		if err := s.Write(u.PCM[i:min(i+n, len(u.PCM))]); err != nil {
			s.Cancel()
			return st, err
		}
		select {
		case <-time.After(time.Until(start.Add(time.Duration(i+n) * time.Second / protocol.SampleRate))):
		case <-ctx.Done():
			s.Cancel()
			return st, ctx.Err()
		}
	}
	eos := time.Now()
	if _, err := s.Finish(ctx); err != nil {
		return st, err
	}
	st.eosFinal = ms(time.Since(eos))
	ts := <-partialAt
	st.partials = len(ts)
	var onset time.Time
	select {
	case onset = <-speechAt:
	default:
	}
	if onset.IsZero() {
		onset = start
	}
	for i, t := range ts {
		if i == 0 {
			st.firstPartial = ms(t.Sub(onset))
		} else {
			st.gaps = append(st.gaps, ms(t.Sub(ts[i-1])))
		}
	}
	return st, nil
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
