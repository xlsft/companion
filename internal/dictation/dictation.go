// Package dictation is raw system dictation (OC-78): the dictation
// shortcut starts recording, the local recogniser transcribes it, and the
// text is typed verbatim into whatever field has focus, in any app. No
// language model sees it and nothing keeps it: the text lives in memory
// between the recogniser and the inserter only, and is never logged.
//
// The shortcut works like Talk's: a tap starts and the next tap stops;
// where the desktop reports key-up, holding it records until release.
package dictation

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Recorder captures one utterance and transcribes it locally.
type Recorder interface {
	// Start opens the microphone; ctx bounds the whole recording.
	Start(ctx context.Context) error
	// Finish stops capture and returns the transcript.
	Finish(ctx context.Context) (string, error)
	// Cancel drops the recording without a transcript.
	Cancel()
}

// Phase is where a dictation is.
type Phase int

const (
	Idle Phase = iota
	Listening
	Transcribing
	Typing
)

func (p Phase) String() string {
	return [...]string{"idle", "listening", "transcribing", "typing"}[p]
}

// Failure codes in State.Err; the UI words them.
const (
	CodeNoSpeech    = "no_speech"   // nothing was recognised
	CodeRecognition = "recognition" // no model, or the recogniser failed
	CodeMicrophone  = "microphone"
	CodeBusy        = "busy"        // Talk is using the microphone
	CodeUnsupported = "unsupported" // this desktop gives no way to type
	CodeDenied      = "denied"      // the system's consent was declined
	CodeInsert      = "insert_failed"
)

// ErrMicrophone marks Recorder.Start failures of the device itself, as
// opposed to the recogniser.
var ErrMicrophone = errors.New("dictation: microphone")

// State is what the UI shows.
type State struct {
	Phase Phase
	// Err is the code of the last failure, "" after a success, until the
	// next dictation starts or the user dismisses it.
	Err string
}

// Config wires a Service.
type Config struct {
	Recorder Recorder
	Inserter contracts.TextInserter
	// Busy reports that Talk holds the microphone right now.
	Busy func() bool
	Log  *slog.Logger
	// MaxDuration ends a recording nobody stops; 0 means 2 minutes.
	MaxDuration time.Duration
	// HoldThreshold is how long the key must be held for its release to
	// stop recording; a shorter tap leaves it on. 0 means 400ms.
	HoldThreshold time.Duration
}

// Service runs one dictation at a time.
type Service struct {
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	base context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu       sync.Mutex
	state    State
	cur      *run
	watchers map[int]func()
	nextW    int
}

// run is one dictation in flight.
type run struct {
	cancel    context.CancelFunc
	finish    chan struct{} // closed to stop listening and transcribe
	finishing bool
	pressedAt time.Time
	held      bool // the key that started it is still down
}

func New(cfg Config) *Service {
	if cfg.MaxDuration == 0 {
		cfg.MaxDuration = 2 * time.Minute
	}
	if cfg.HoldThreshold == 0 {
		cfg.HoldThreshold = 400 * time.Millisecond
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Service{cfg: cfg, log: log, now: time.Now, base: ctx, stop: stop, watchers: map[int]func(){}}
}

// Hotkey handles the dictation shortcut.
func (s *Service) Hotkey(kind contracts.HotkeyEventKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case contracts.HotkeyToggled:
		s.toggleLocked(false)
	case contracts.HotkeyPressed:
		s.toggleLocked(true)
	case contracts.HotkeyReleased:
		r := s.cur
		if r == nil || !r.held {
			return
		}
		r.held = false
		if s.state.Phase == Listening && s.now().Sub(r.pressedAt) >= s.cfg.HoldThreshold {
			s.finishLocked(r)
		}
	}
}

// Toggle starts dictation or, while listening, stops it.
func (s *Service) Toggle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toggleLocked(false)
}

func (s *Service) toggleLocked(held bool) {
	switch {
	case s.cur == nil:
		s.startLocked(held)
	case s.state.Phase == Listening:
		s.finishLocked(s.cur)
	}
	// Transcribing or typing: the press has nothing to stop.
}

// Cancel drops a dictation in flight, at any phase; it reports whether
// there was one, so Cancel's shortcut can serve Talk otherwise.
func (s *Service) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return false
	}
	s.cur.cancel()
	return true
}

// Active reports whether a dictation is in flight.
func (s *Service) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Dismiss clears a shown failure.
func (s *Service) Dismiss() {
	s.mu.Lock()
	if s.state.Err == "" {
		s.mu.Unlock()
		return
	}
	s.state.Err = ""
	s.notifyLocked()
}

// Watch calls fn after every state change until cancelled. fn runs on the
// service's goroutines and must not block.
func (s *Service) Watch(fn func()) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextW
	s.nextW++
	s.watchers[id] = fn
	return func() {
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
	}
}

// Close cancels a dictation in flight and waits for it.
func (s *Service) Close() {
	s.stop()
	s.wg.Wait()
}

// notifyLocked unlocks s.mu and tells the watchers.
func (s *Service) notifyLocked() {
	ws := make([]func(), 0, len(s.watchers))
	for _, w := range s.watchers {
		ws = append(ws, w)
	}
	s.mu.Unlock()
	for _, w := range ws {
		w()
	}
}

func (s *Service) setPhase(r *run, p Phase, code string) {
	s.mu.Lock()
	if s.cur != r {
		s.mu.Unlock()
		return
	}
	s.state = State{Phase: p, Err: code}
	if p == Idle {
		s.cur = nil
	}
	s.notifyLocked()
}

func (s *Service) startLocked(held bool) {
	if s.base.Err() != nil {
		return
	}
	if s.cfg.Busy != nil && s.cfg.Busy() {
		s.state = State{Phase: Idle, Err: CodeBusy}
		s.log.Info("dictation", "outcome", CodeBusy)
		go s.notifyAll()
		return
	}
	ctx, cancel := context.WithCancel(s.base)
	r := &run{cancel: cancel, finish: make(chan struct{}), pressedAt: s.now(), held: held}
	s.cur = r
	s.state = State{Phase: Listening}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.work(ctx, r)
	}()
	go s.notifyAll()
}

func (s *Service) notifyAll() {
	s.mu.Lock()
	s.notifyLocked()
}

func (s *Service) finishLocked(r *run) {
	if !r.finishing {
		r.finishing = true
		close(r.finish)
	}
}

func (s *Service) work(ctx context.Context, r *run) {
	rec := s.cfg.Recorder
	if err := rec.Start(ctx); err != nil {
		s.end(ctx, r, failCode(err), "start")
		return
	}
	limit := time.NewTimer(s.cfg.MaxDuration)
	defer limit.Stop()
	select {
	case <-r.finish:
	case <-limit.C:
	case <-ctx.Done():
		rec.Cancel()
		s.end(ctx, r, "", "cancelled")
		return
	}
	s.setPhase(r, Transcribing, "")
	text, err := rec.Finish(ctx)
	if err != nil {
		s.end(ctx, r, failCode(err), "transcribe")
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		s.end(ctx, r, CodeNoSpeech, "empty")
		return
	}
	s.setPhase(r, Typing, "")
	err = s.cfg.Inserter.InsertText(ctx, text)
	s.end(ctx, r, insertCode(err), "insert")
}

// end returns to Idle with code. A cancelled dictation shows no failure.
func (s *Service) end(ctx context.Context, r *run, code, step string) {
	if ctx.Err() != nil {
		code = ""
		step = "cancelled"
	}
	outcome := code
	if outcome == "" {
		outcome = "ok"
		if step == "cancelled" {
			outcome = step
		}
	}
	// Technical codes only: never the text or its length.
	s.log.Info("dictation", "outcome", outcome, "step", step)
	s.setPhase(r, Idle, code)
}

func failCode(err error) string {
	if errors.Is(err, ErrMicrophone) {
		return CodeMicrophone
	}
	return CodeRecognition
}

func insertCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, contracts.ErrPermissionDenied):
		return CodeDenied
	case errors.Is(err, contracts.ErrUnsupported):
		return CodeUnsupported
	}
	return CodeInsert
}
