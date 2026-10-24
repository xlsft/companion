// Package fake is a deterministic engine for protocol and client tests. Its
// transcript is derived only from the audio length, so a test can tell that
// every chunk arrived without running a model.
package fake

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/xlsft/companion/internal/stt/engine"
	"github.com/xlsft/companion/internal/stt/protocol"
)

type Options struct {
	// Delay is added to every decode and honours cancellation, to exercise
	// cancel and health-during-decode paths.
	Delay time.Duration
	// ExitOnFinal terminates the process during the first final decode,
	// simulating a native crash in the runtime.
	ExitOnFinal bool
	// Streaming makes the engine a streaming one (engine.Streamer): the
	// hypothesis is Text(true, samples so far), the final Text(false, all).
	Streaming bool
}

const RuntimeVersion = "fake-1"

func Factory(opts Options) engine.Factory {
	return func(engine.Config) (engine.Engine, engine.Info, error) {
		info := engine.Info{
			RuntimeVersion: RuntimeVersion,
			BackendInUse:   protocol.BackendCPU,
			Languages:      []string{"en", "ru"},
		}
		return &Engine{opts: opts}, info, nil
	}
}

type Engine struct {
	opts Options
}

// Text returns the transcript the fake produces for n samples, so tests can
// compute the expected value.
func Text(partial bool, n int) string {
	kind := "final"
	if partial {
		kind = "partial"
	}
	return fmt.Sprintf("%s %dms", kind, Duration(n).Milliseconds())
}

func Duration(n int) time.Duration {
	return time.Duration(n) * time.Second / protocol.SampleRate
}

func (e *Engine) Transcribe(ctx context.Context, pcm []float32, opts engine.Options) (engine.Result, error) {
	if !opts.Partial && e.opts.ExitOnFinal {
		os.Exit(3)
	}
	if e.opts.Delay > 0 {
		t := time.NewTimer(e.opts.Delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return engine.Result{}, ctx.Err()
		case <-t.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return engine.Result{}, err
	}
	lang := opts.Language
	if lang == "" || lang == protocol.LanguageAuto {
		lang = "en"
	}
	text := Text(opts.Partial, len(pcm))
	return engine.Result{
		Text:     text,
		Language: lang,
		Segments: []engine.Segment{{Start: 0, End: Duration(len(pcm)), Text: text}},
	}, nil
}

func (e *Engine) Close() error { return nil }

func (e *Engine) NewStream(opts engine.Options) (engine.Stream, error) {
	if !e.opts.Streaming {
		return nil, engine.ErrNotStreaming
	}
	return &stream{e: e, lang: opts.Language}, nil
}

type stream struct {
	e    *Engine
	lang string
	n    int
}

func (s *stream) Accept(ctx context.Context, pcm []float32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.n += len(pcm)
	return Text(true, s.n), nil
}

func (s *stream) Finish(ctx context.Context) (engine.Result, error) {
	return s.e.Transcribe(ctx, make([]float32, s.n), engine.Options{Language: s.lang})
}

func (s *stream) Close() error { return nil }
