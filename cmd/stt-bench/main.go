// Command stt-bench drives the STT sidecar over its protocol and reports
// latency, memory and accuracy per model × language × backend (OC-37).
// Models and audio are supplied by the caller and never committed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/stt/bench"
)

func main() {
	var (
		sidecar  = flag.String("sidecar", "bin/stt-sidecar", "whisper-enabled sidecar binary (make whisper build-whisper)")
		modelDir = flag.String("model-dir", "", "directory with the catalogue model files (required)")
		ids      = flag.String("models", "whisper-tiny,whisper-base,whisper-small", "comma-separated catalogue ids")
		corpus   = flag.String("corpus", "", "directory with corpus.tsv (lang, wav path, reference) (required)")
		langs    = flag.String("langs", "en,ru", "languages to run")
		backends = flag.String("backends", "cpu", "comma-separated: cpu, auto")
		batchN   = flag.Int("n", 30, "unpaced utterances per language (quality, RTF)")
		liveN    = flag.Int("live", 4, "real-time paced utterances per language (latency)")
		threads  = flag.Int("threads", 0, "decode threads, 0 = sidecar default")
		jsonOut  = flag.String("json", "", "write the JSON report here")
		mdOut    = flag.String("md", "", "write the markdown tables here (default stdout)")
		peak     = flag.Float64("peak", 0.9, "peak-normalise each clip to this fraction of full scale, 0 = off")
		vad      = flag.Bool("vad", true, "keep the sidecar VAD gate on, as in the product; false decodes every clip")
		details  = flag.Bool("details", false, "include per-utterance transcripts in the JSON (contains text)")
	)
	flag.Parse()
	if *modelDir == "" || *corpus == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *sidecar, *modelDir, *ids, *corpus, *langs, *backends, *batchN, *liveN, *threads, *jsonOut, *mdOut, *details, *peak, *vad); err != nil {
		fmt.Fprintln(os.Stderr, "stt-bench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, sidecar, modelDir, ids, corpusDir, langs, backends string, batchN, liveN, threads int, jsonOut, mdOut string, details bool, peak float64, vad bool) error {
	cat, err := models.BuiltinCatalog()
	if err != nil {
		return err
	}
	langList := split(langs)
	want := map[string]int{}
	for _, l := range langList {
		want[l] = batchN + liveN
	}
	all, err := bench.LoadCorpus(corpusDir, want, peak)
	if err != nil {
		return err
	}
	byLang := map[string][]bench.Utterance{}
	corpusInfo := map[string]any{"dir_name": filepath.Base(corpusDir)}
	for _, u := range all {
		byLang[u.Lang] = append(byLang[u.Lang], u)
	}
	for l, us := range byLang {
		var d time.Duration
		for _, u := range us {
			d += u.Duration()
		}
		corpusInfo[l+"_utterances"] = len(us)
		corpusInfo[l+"_audio_s"] = d.Seconds()
	}

	prof := hw.Detect(ctx, modelDir)
	rep := bench.Report{
		Date:    time.Now().UTC().Format(time.RFC3339),
		Machine: map[string]any{"profile": prof},
		Corpus:  corpusInfo,
		Method: map[string]any{
			"batch_per_lang": batchN, "live_per_lang": liveN, "threads": threads,
			"live_chunk_ms": 100, "peak_normalise": peak, "sidecar_vad": vad,
		},
	}

	for _, id := range split(ids) {
		m, ok := cat.Find(id)
		if !ok {
			return fmt.Errorf("model %q not in the catalogue", id)
		}
		path := filepath.Join(modelDir, m.File)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		for _, lang := range langList {
			if lang != "en" && !m.Multilingual() {
				continue // an English-only model cannot be scored on other languages
			}
			us := byLang[lang]
			if len(us) == 0 {
				continue
			}
			// Live utterances come from the tail so they differ from the
			// batch set only when the corpus is big enough to allow it;
			// otherwise they overlap, which is harmless for latency.
			b := us[:min(batchN, len(us))]
			l := us[max(0, len(us)-liveN):]
			for _, be := range split(backends) {
				fmt.Fprintf(os.Stderr, "== %s lang=%s backend=%s\n", id, lang, be)
				r, err := bench.Run(ctx, bench.Config{
					Sidecar: sidecar, SidecarArgs: []string{fmt.Sprintf("-vad=%v", vad)}, ModelPath: path, ModelSHA256: m.SHA256,
					Lang: lang, Backend: be, Threads: threads, Batch: b, Live: l,
					Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) },
				}, details)
				r.Model = id
				if err != nil {
					r.Error = err.Error()
					fmt.Fprintln(os.Stderr, "  failed:", err)
				}
				rep.Results = append(rep.Results, r)
				if ctx.Err() != nil {
					break
				}
			}
		}
	}

	if jsonOut != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	out := os.Stdout
	if mdOut != "" {
		f, err := os.Create(mdOut)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	bench.WriteMarkdown(out, rep.Results)
	return nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
