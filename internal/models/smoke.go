package models

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/protocol"
	"github.com/xlsft/companion/internal/stt/supervisor"
)

// SmokeSignal is the bundled test input: 1 s of silence followed by 1 s of
// a 440 Hz tone. Nothing about the transcript is asserted; the signal only
// has to exercise load, streaming and the final decode.
func SmokeSignal() []int16 {
	const rate = protocol.SampleRate
	out := make([]int16, 2*rate)
	for i := rate; i < len(out); i++ {
		out[i] = int16(0.3 * math.MaxInt16 * math.Sin(2*math.Pi*440*float64(i)/rate))
	}
	return out
}

// SidecarSmoke tests a model through a supervised sidecar process, i.e. the
// exact path the app uses at runtime.
type SidecarSmoke struct {
	// Path and Args start the sidecar binary (e.g. -engine whisper).
	Path string
	Args []string
	// Language and Backend default to "auto" and "cpu".
	Language string
	Backend  string
	// Timeout bounds the whole test including model load; default 2 min.
	Timeout time.Duration
	// Supervisor tunes restart behaviour; zero values give a short, strict
	// policy (one restart) so a broken model fails fast.
	Supervisor supervisor.Config
}

func (s SidecarSmoke) Smoke(ctx context.Context, m InstalledModel) error {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lang, backend := s.Language, s.Backend
	if lang == "" {
		lang = protocol.LanguageAuto
	}
	if backend == "" {
		backend = protocol.BackendCPU
	}
	cfg := s.Supervisor
	cfg.Launcher = func(lctx context.Context) (*stt.Client, error) {
		return stt.Start(lctx, stt.Config{
			Path: s.Path,
			Args: s.Args,
			Init: protocol.InitializeParams{
				ModelPath:   m.Path,
				ModelSHA256: m.SHA256,
				Language:    lang,
				Backend:     backend,
			},
		})
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 1
	}
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = 100 * time.Millisecond
	}
	sup := supervisor.New(cfg)
	sup.Start(ctx)
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = sup.Stop(sctx)
	}()

	if err := sup.WaitReady(ctx); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	sess, err := sup.StartSession(ctx, stt.WithPartials(false))
	if err != nil {
		return fmt.Errorf("session start: %w", err)
	}
	if err := sess.Write(SmokeSignal()); err != nil {
		sess.Cancel()
		return fmt.Errorf("session write: %w", err)
	}
	if _, err := sess.Finish(ctx); err != nil {
		return fmt.Errorf("session finish: %w", err)
	}
	return nil
}
