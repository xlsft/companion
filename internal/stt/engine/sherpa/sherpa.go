//go:build sherpa && cgo

package sherpa

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	so "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/xlsft/companion/internal/stt/engine"
	"github.com/xlsft/companion/internal/stt/protocol"
)

func init() { engine.Register("sherpa", New) }

// maxThreads: onnxruntime's int8 kernels gain little beyond four threads
// on desktop CPUs, while every extra thread makes the fans louder.
const maxThreads = 4

// tailPadding flushes a streaming model's look-ahead at the end of an
// utterance, as sherpa-onnx's own examples do.
const tailPadding = protocol.SampleRate * 3 / 10

type Engine struct {
	desc    engine.Descriptor
	lang    string // initialize language
	threads int

	// mu serialises native calls: a decode abandoned on cancellation still
	// owns the recogniser until it returns, and Close must wait for it.
	mu     sync.Mutex
	off    *so.OfflineRecognizer
	on     *so.OnlineRecognizer
	closed bool
}

func New(cfg engine.Config) (engine.Engine, engine.Info, error) {
	info := engine.Info{RuntimeVersion: "sherpa-onnx " + so.GetVersion(), BackendInUse: protocol.BackendCPU}
	if cfg.ModelPath == "" {
		return nil, info, engine.ErrModelNotFound
	}
	st, err := os.Stat(cfg.ModelPath)
	if err != nil {
		return nil, info, fmt.Errorf("%w: %v", engine.ErrModelNotFound, err)
	}
	if !st.IsDir() {
		return nil, info, fmt.Errorf("%w: sherpa models are directories", engine.ErrBackendInit)
	}
	desc, err := engine.ReadDescriptor(cfg.ModelPath)
	if errors.Is(err, os.ErrNotExist) {
		var ok bool
		if desc, ok = Detect(cfg.ModelPath); !ok {
			return nil, info, fmt.Errorf("%w: unrecognised model directory", engine.ErrBackendInit)
		}
	} else if err != nil {
		return nil, info, fmt.Errorf("%w: %v", engine.ErrBackendInit, err)
	}
	for role := range desc.Files {
		if _, err := os.Stat(desc.Path(cfg.ModelPath, role)); err != nil {
			return nil, info, fmt.Errorf("%w: %s missing", engine.ErrModelNotFound, role)
		}
	}

	e := &Engine{desc: desc, lang: cfg.Language, threads: cfg.Threads}
	if e.threads <= 0 {
		e.threads = min(runtime.NumCPU(), maxThreads)
	}
	dir := cfg.ModelPath
	switch desc.Type {
	case TypeNemoTransducer, TypeSenseVoice:
		c := so.OfflineRecognizerConfig{
			FeatConfig:     so.FeatureConfig{SampleRate: protocol.SampleRate, FeatureDim: 80},
			DecodingMethod: "greedy_search",
		}
		c.ModelConfig.Tokens = desc.Path(dir, engine.RoleTokens)
		c.ModelConfig.NumThreads = e.threads
		c.ModelConfig.Provider = "cpu"
		if desc.Type == TypeNemoTransducer {
			c.ModelConfig.ModelType = "nemo_transducer"
			c.ModelConfig.Transducer = so.OfflineTransducerModelConfig{
				Encoder: desc.Path(dir, engine.RoleEncoder),
				Decoder: desc.Path(dir, engine.RoleDecoder),
				Joiner:  desc.Path(dir, engine.RoleJoiner),
			}
		} else {
			c.ModelConfig.SenseVoice = so.OfflineSenseVoiceModelConfig{
				Model:                       desc.Path(dir, engine.RoleModel),
				Language:                    senseVoiceHint(cfg.Language),
				UseInverseTextNormalization: 1,
			}
		}
		if e.off = so.NewOfflineRecognizer(&c); e.off == nil {
			return nil, info, fmt.Errorf("%w: offline recognizer", engine.ErrBackendInit)
		}
	case TypeZipformerTransducer:
		c := so.OnlineRecognizerConfig{
			FeatConfig:     so.FeatureConfig{SampleRate: protocol.SampleRate, FeatureDim: 80},
			DecodingMethod: "greedy_search",
			// Endpointing stays with the sidecar's VAD: its semantics
			// (end_silence_ms, leading silence, noise floor) are the
			// protocol's and must not depend on the engine.
			EnableEndpoint: 0,
		}
		c.ModelConfig.Transducer = so.OnlineTransducerModelConfig{
			Encoder: desc.Path(dir, engine.RoleEncoder),
			Decoder: desc.Path(dir, engine.RoleDecoder),
			Joiner:  desc.Path(dir, engine.RoleJoiner),
		}
		c.ModelConfig.Tokens = desc.Path(dir, engine.RoleTokens)
		c.ModelConfig.NumThreads = e.threads
		c.ModelConfig.Provider = "cpu"
		if e.on = so.NewOnlineRecognizer(&c); e.on == nil {
			return nil, info, fmt.Errorf("%w: online recognizer", engine.ErrBackendInit)
		}
	default:
		return nil, info, fmt.Errorf("%w: model type %q", engine.ErrBackendInit, desc.Type)
	}
	info.Languages = slices.Clone(desc.Languages)
	return e, info, nil
}

// result is what a native decode hands back to the waiting caller.
type result struct {
	res engine.Result
	err error
}

// run executes fn under mu on its own goroutine and returns as soon as ctx
// is cancelled. sherpa-onnx has no abort hook, so a cancelled decode runs
// to completion in the background (a few hundred ms for a 10 s window) and
// the next call queues behind it.
func (e *Engine) run(ctx context.Context, fn func() result) (engine.Result, error) {
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	done := make(chan result, 1)
	go func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		switch {
		case e.closed:
			done <- result{err: errors.New("sherpa: engine closed")}
		case ctx.Err() != nil:
			done <- result{err: ctx.Err()}
		default:
			done <- fn()
		}
	}()
	select {
	case <-ctx.Done():
		return engine.Result{}, ctx.Err()
	case r := <-done:
		return r.res, r.err
	}
}

func (e *Engine) Transcribe(ctx context.Context, pcm []float32, opts engine.Options) (engine.Result, error) {
	audio := time.Duration(len(pcm)) * time.Second / protocol.SampleRate
	return e.run(ctx, func() result {
		if e.on != nil {
			s := so.NewOnlineStream(e.on)
			defer so.DeleteOnlineStream(s)
			s.AcceptWaveform(protocol.SampleRate, pcm)
			s.AcceptWaveform(protocol.SampleRate, make([]float32, tailPadding))
			s.InputFinished()
			for e.on.IsReady(s) {
				e.on.Decode(s)
			}
			return result{res: e.finalize(e.on.GetResult(s).Text, "", opts.Language, audio)}
		}
		s := so.NewOfflineStream(e.off)
		defer so.DeleteOfflineStream(s)
		s.AcceptWaveform(protocol.SampleRate, pcm)
		e.off.Decode(s)
		r := s.GetResult()
		return result{res: e.finalize(r.Text, r.Lang, opts.Language, audio)}
	})
}

// finalize builds the result. Only SenseVoice reports a language; for the
// others a requested language is echoed and "auto" stays unresolved, which
// the sidecar reports as the session's language.
func (e *Engine) finalize(text, langTag, want string, audio time.Duration) engine.Result {
	text = normalize(text)
	res := engine.Result{Text: text}
	if l := senseVoiceLang(langTag); l != "" {
		res.Language = l
	} else if want != "" && want != protocol.LanguageAuto {
		res.Language = want
	}
	if text != "" {
		res.Segments = []engine.Segment{{Start: 0, End: audio, Text: text}}
	}
	return res
}

// NewStream implements engine.Streamer for streaming models.
func (e *Engine) NewStream(opts engine.Options) (engine.Stream, error) {
	if e.on == nil {
		return nil, engine.ErrNotStreaming
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("sherpa: engine closed")
	}
	return &stream{e: e, s: so.NewOnlineStream(e.on), lang: opts.Language}, nil
}

type stream struct {
	e       *Engine
	s       *so.OnlineStream
	lang    string
	samples int
}

func (st *stream) Accept(ctx context.Context, pcm []float32) (string, error) {
	st.samples += len(pcm)
	r, err := st.e.run(ctx, func() result {
		st.s.AcceptWaveform(protocol.SampleRate, pcm)
		for st.e.on.IsReady(st.s) {
			st.e.on.Decode(st.s)
		}
		return result{res: engine.Result{Text: normalize(st.e.on.GetResult(st.s).Text)}}
	})
	return r.Text, err
}

func (st *stream) Finish(ctx context.Context) (engine.Result, error) {
	audio := time.Duration(st.samples) * time.Second / protocol.SampleRate
	return st.e.run(ctx, func() result {
		st.s.AcceptWaveform(protocol.SampleRate, make([]float32, tailPadding))
		st.s.InputFinished()
		for st.e.on.IsReady(st.s) {
			st.e.on.Decode(st.s)
		}
		return result{res: st.e.finalize(st.e.on.GetResult(st.s).Text, "", st.lang, audio)}
	})
}

// Close frees the native stream once no decode of it is running.
func (st *stream) Close() error {
	st.e.mu.Lock()
	defer st.e.mu.Unlock()
	if st.s != nil {
		so.DeleteOnlineStream(st.s)
		st.s = nil
	}
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if e.off != nil {
		so.DeleteOfflineRecognizer(e.off)
		e.off = nil
	}
	if e.on != nil {
		so.DeleteOnlineRecognizer(e.on)
		e.on = nil
	}
	return nil
}

var _ engine.Streamer = (*Engine)(nil)
