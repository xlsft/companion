package setupcheck

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/stt"
	"github.com/xlsft/companion/internal/stt/supervisor"
	"github.com/xlsft/companion/internal/talk"
)

// Listener records one utterance from the selected microphone through the
// local recogniser, ending at the sidecar's end-of-utterance. Audio goes
// from the device to the sidecar in memory only (FR-VOICE-012).
type Listener struct {
	Mic    audio.Capturer
	Device func() string
	STT    talk.STT
	// Model reports whether a model is active; nil skips the check.
	Model func() error
	// Ready waits until the recogniser can take sessions; nil skips it.
	Ready        func(ctx context.Context) error
	ReadyTimeout time.Duration // default 90 s (model load)
	// Window bounds the recording (default 10 s); leading silence ends it
	// after LeadingSilence (default 6 s).
	Window         time.Duration
	LeadingSilence time.Duration
	FinishTimeout  time.Duration // default 30 s
}

// Stages of a Listen failure.
const (
	StageSpeech = "speech"
	StageSTT    = "stt"
)

// Heard is a recognised utterance. Text is user content.
type Heard struct {
	Text string
}

// Failure is a failed stage with its code.
type Failure struct {
	Stage, Code string
}

func (l *Listener) Listen(ctx context.Context, lang string) (Heard, *Failure) {
	return l.listen(ctx, lang, listenHooks{})
}

// listenHooks let the loopback play while the microphone records.
type listenHooks struct {
	opened func()          // the microphone records; called once
	stop   <-chan struct{} // closed: end the recording now and decode it
	window time.Duration   // overrides Window and LeadingSilence when set
}

func (l *Listener) listen(ctx context.Context, lang string, h listenHooks) (Heard, *Failure) {
	sttFail := func(code string) (Heard, *Failure) { return Heard{}, &Failure{StageSTT, code} }
	micFail := func(code string) (Heard, *Failure) { return Heard{}, &Failure{StageSpeech, code} }
	if l.Model != nil && l.Model() != nil {
		return sttFail(CodeNoModel)
	}
	if l.Ready != nil {
		rctx, cancel := within(ctx, l.ReadyTimeout, 90*time.Second)
		err := l.Ready(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return sttFail(CodeCanceled)
			}
			return sttFail(CodeSTTUnavailable)
		}
	}
	window := l.Window
	if window <= 0 {
		window = 10 * time.Second
	}
	lead := l.LeadingSilence
	if lead <= 0 {
		lead = 6 * time.Second
	}
	if h.window > 0 {
		window, lead = h.window, h.window
	}
	opts := []stt.SessionOption{stt.WithVAD(stt.VADOptions{MaxLeadingSilence: lead, MaxDuration: window})}
	if lang != "" {
		opts = append(opts, stt.WithLanguage(lang))
	}
	sess, err := l.STT(ctx, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return sttFail(CodeCanceled)
		}
		return sttFail(CodeSTTUnavailable)
	}
	recCtx, stopRec := context.WithCancel(ctx)
	defer stopRec()
	dev := ""
	if l.Device != nil {
		dev = l.Device()
	}
	stream, err := l.Mic.Open(recCtx, dev)
	if err != nil {
		sess.Cancel()
		return micFail(micCode(err))
	}
	pumped := make(chan error, 1)
	go func() {
		var werr error
		for f := range stream.Frames() {
			if werr == nil {
				werr = sess.Write(f.Samples)
			}
		}
		pumped <- werr
	}()
	if h.opened != nil {
		h.opened()
	}

	// The sidecar's endpointer ends the utterance; the timer is a backstop
	// a little past its MaxDuration.
	timer := time.NewTimer(window + 2*time.Second)
	defer timer.Stop()
	var pumpErr error
	pumpDone := false
	select {
	case <-sess.Endpoint():
	case <-timer.C:
	case pumpErr = <-pumped:
		pumpDone = true
	case <-h.stop:
	case <-ctx.Done():
	}
	_ = stream.Close()
	if !pumpDone {
		pumpErr = <-pumped
	}
	if ctx.Err() != nil {
		sess.Cancel()
		return sttFail(CodeCanceled)
	}
	if err := stream.Err(); errors.Is(err, audio.ErrDeviceLost) {
		sess.Cancel()
		return micFail(CodeMicLost)
	}
	if pumpErr != nil {
		sess.Cancel()
		return sttFail(sessionCode(pumpErr))
	}
	fctx, cancel := within(ctx, l.FinishTimeout, 30*time.Second)
	defer cancel()
	final, err := sess.Finish(fctx)
	if err != nil {
		if ctx.Err() != nil {
			return sttFail(CodeCanceled)
		}
		return sttFail(sessionCode(err))
	}
	text := strings.TrimSpace(final.Text)
	if !final.Speech || text == "" {
		return micFail(CodeNoSpeech)
	}
	return Heard{Text: text}, nil
}

func sessionCode(err error) string {
	if errors.Is(err, supervisor.ErrInterrupted) || errors.Is(err, stt.ErrSidecarExited) {
		return CodeSTTUnavailable
	}
	return CodeSTTFailed
}

// STT is the StepSTT check: the user reads readiness.MicPhrase(lang) and
// the transcript must match it loosely (readiness.PhraseMatches). A
// Loopback match with the same settings within Reuse passes it without a
// second recording (OC-133).
type STT struct {
	Listener *Listener
	Lang     func() string // UI language
	// Key names the settings a loopback match holds for (microphone,
	// model, voice, language); nil never reuses one.
	Key   func() string
	Reuse time.Duration // default 2 min
	Now   func() time.Time

	mu      sync.Mutex
	last    string
	loopAt  time.Time // when a loopback last matched; zero: none
	loopKey string
}

// DefaultLoopbackReuse is how long a loopback match passes StepSTT.
const DefaultLoopbackReuse = 2 * time.Minute

func (s *STT) Run(ctx context.Context) readiness.Result {
	if s.loopbackFresh() {
		return readiness.Result{Status: readiness.Passed,
			Detail: map[string]string{readiness.DetailSource: readiness.SourceLoopback}}
	}
	s.setLast("")
	lang := s.Lang()
	heard, f := s.Listener.Listen(ctx, readiness.PhraseLanguage(lang))
	if f != nil {
		return failed(f.Code)
	}
	s.setLast(heard.Text)
	if !readiness.PhraseMatches(readiness.MicPhrase(lang), heard.Text) {
		return failed(CodeMismatch)
	}
	return readiness.Result{Status: readiness.Passed,
		Detail: map[string]string{readiness.DetailSource: readiness.SourceUser}}
}

func (s *STT) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *STT) key() string {
	if s.Key == nil {
		return ""
	}
	return s.Key()
}

// loopbackHeard records what a loopback heard; a match is remembered for
// the current settings.
func (s *STT) loopbackHeard(text string, matched bool) {
	key := s.key()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = text
	if matched && s.Key != nil {
		s.loopAt, s.loopKey = s.now(), key
	}
}

func (s *STT) forgetLoopback() {
	s.mu.Lock()
	s.last, s.loopAt, s.loopKey = "", time.Time{}, ""
	s.mu.Unlock()
}

func (s *STT) loopbackFresh() bool {
	if s.Key == nil {
		return false
	}
	key, now := s.key(), s.now()
	reuse := s.Reuse
	if reuse <= 0 {
		reuse = DefaultLoopbackReuse
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.loopAt.IsZero() && s.loopKey == key && now.Sub(s.loopAt) <= reuse && now.Sub(s.loopAt) >= 0
}

// LastTranscript is what the last run heard, this check's own or a
// Loopback's, whichever ran last; memory only, never logged.
func (s *STT) LastTranscript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

func (s *STT) setLast(t string) {
	s.mu.Lock()
	s.last = t
	s.mu.Unlock()
}
