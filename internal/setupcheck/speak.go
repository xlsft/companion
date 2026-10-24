package setupcheck

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/talk"
)

// Speak is the StepTTS check: it plays readiness.TTSSentence(lang) with the
// saved provider, voice and speed through the output device.
type Speak struct {
	Speaker talk.Speaker
	// Configured reports whether a provider and key are saved.
	Configured func() bool
	Lang       func() string
	Timeout    time.Duration // default 45 s
}

func (s Speak) Run(ctx context.Context) readiness.Result {
	if code := say(ctx, s.Speaker, s.Configured, readiness.TTSSentence(s.Lang()), s.Timeout); code != "" {
		return failed(code)
	}
	return readiness.Result{Status: readiness.Passed}
}

// say speaks text and returns a failure code, "" when audio reached the
// device and played to the end.
func say(ctx context.Context, sp talk.Speaker, configured func() bool, text string, timeout time.Duration) string {
	if sp == nil || configured == nil || !configured() {
		return CodeNotConfigured
	}
	ctx, cancel := within(ctx, timeout, 45*time.Second)
	defer cancel()
	var started atomic.Bool
	err := sp.Say(ctx, text, func() { started.Store(true) })
	switch {
	case err != nil:
		return ttsCode(err)
	case !started.Load():
		return CodePlayback // nothing became audible
	}
	return ""
}
