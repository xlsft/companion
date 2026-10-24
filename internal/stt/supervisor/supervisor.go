// Package supervisor keeps the STT sidecar alive (OC-36, FR-STT-010,
// NFR-003): it launches the process, health-checks it, restarts it with
// exponential backoff and gives up into a Degraded state after a crash loop.
// A sidecar crash must never take the main app down, so every failure is
// turned into a state change or a typed error, never a panic or exit.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/stt"
)

var (
	// ErrNotReady means no healthy sidecar exists right now (starting or
	// restarting); callers may retry shortly.
	ErrNotReady = errors.New("stt supervisor: sidecar not ready")
	// ErrDegraded means the crash-loop limit was hit; nothing is retried
	// until Reset.
	ErrDegraded = errors.New("stt supervisor: degraded")
	// ErrStopped is returned after Stop.
	ErrStopped = errors.New("stt supervisor: stopped")
	// ErrInterrupted marks a request that was in flight when the sidecar
	// died. It is never re-sent automatically; the UI asks the user to
	// repeat it (PRD §13).
	ErrInterrupted = errors.New("stt supervisor: request interrupted, repeat the request")
	// ErrPermanent can be wrapped by a Launcher error to say that retrying
	// is pointless (missing model, checksum mismatch): the supervisor goes
	// straight to Degraded.
	ErrPermanent = errors.New("stt supervisor: permanent launch failure")
)

// Failure and reason codes. They are technical codes only (ADR-0001).
const (
	ReasonSidecarExited = "sidecar_exited"
	ReasonHealthTimeout = "health_timeout"
	ReasonHealthFailed  = "health_failed"
	ReasonLaunchFailed  = "launch_failed"
	ReasonLaunchRefused = "launch_refused"
	ReasonCrashLoop     = "crash_loop"
	ReasonReset         = "reset"
)

// Launcher starts and initializes a sidecar. ctx bounds the process
// lifetime (stt.Start semantics); it is cancelled when the supervisor is
// done with the process. The launcher reads the current model settings on
// every call, so Reset after a model change picks the new model up.
type Launcher func(ctx context.Context) (*stt.Client, error)

type Config struct {
	Launcher Launcher
	// HealthInterval and HealthTimeout default to 10 s and 2 s.
	HealthInterval time.Duration
	HealthTimeout  time.Duration
	// LaunchTimeout bounds Launcher (model load included); default 60 s.
	LaunchTimeout time.Duration
	// BackoffInitial and BackoffMax default to 500 ms and 30 s.
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	// More than MaxRestarts failures within CrashWindow degrade; defaults
	// 5 in 2 min.
	MaxRestarts int
	CrashWindow time.Duration
	// StableAfter is how long a process must stay healthy before the
	// backoff sequence starts over; default 1 min.
	StableAfter time.Duration
	// StopTimeout bounds the graceful shutdown of the sidecar; default 2 s.
	StopTimeout time.Duration
	Logger      *slog.Logger
}

func (c *Config) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	set(&c.HealthInterval, 10*time.Second)
	set(&c.HealthTimeout, 2*time.Second)
	set(&c.LaunchTimeout, 60*time.Second)
	set(&c.BackoffInitial, 500*time.Millisecond)
	set(&c.BackoffMax, 30*time.Second)
	set(&c.CrashWindow, 2*time.Minute)
	set(&c.StableAfter, time.Minute)
	set(&c.StopTimeout, 2*time.Second)
	if c.MaxRestarts <= 0 {
		c.MaxRestarts = 5
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
}

// Backoff returns the delay before restart attempt n (1-based).
func (c Config) Backoff(n int) time.Duration {
	d := c.BackoffInitial
	for i := 1; i < n && d < c.BackoffMax; i++ {
		d *= 2
	}
	return min(d, c.BackoffMax)
}

type Kind int

const (
	Stopped Kind = iota
	Starting
	Ready
	Restarting
	Degraded
)

func (k Kind) String() string {
	return [...]string{"stopped", "starting", "ready", "restarting", "degraded"}[k]
}

// State is published to subscribers. Reason is a technical code: the cause
// of the restart for Restarting, ReasonCrashLoop (or ReasonLaunchRefused)
// for Degraded.
type State struct {
	Kind Kind
	// Attempt counts consecutive restarts (Restarting only).
	Attempt int
	Reason  string
	// LastFailure is the code of the failure that led to Degraded.
	LastFailure string
}

func (s State) String() string {
	return fmt.Sprintf("%s attempt=%d reason=%s", s.Kind, s.Attempt, s.Reason)
}

type Supervisor struct {
	cfg Config

	mu      sync.Mutex
	state   State
	client  *stt.Client
	subs    map[int]chan State
	nextSub int
	started bool
	cancel  context.CancelFunc
	done    chan struct{}

	reset chan struct{}
}

func New(cfg Config) *Supervisor {
	cfg.defaults()
	return &Supervisor{
		cfg:   cfg,
		subs:  map[int]chan State{},
		reset: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// Start launches the supervision loop and returns immediately; watch
// Subscribe or WaitReady for the outcome. ctx bounds the supervisor's life
// like Stop. Calling Start twice is a no-op.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()
	s.publish(State{Kind: Starting})
	go s.run(ctx)
}

// Stop shuts the sidecar down and ends the loop. It waits until everything
// is gone or ctx expires. Safe to call repeatedly and before Start.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.started = true
		s.cancel = func() {}
		s.mu.Unlock()
		s.finish()
		return nil
	}
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reset leaves Degraded (or forces a fresh process in any other state, e.g.
// after a model change) and clears the crash history. In-flight sessions
// fail with ErrInterrupted.
func (s *Supervisor) Reset() {
	select {
	case s.reset <- struct{}{}:
	default:
	}
}

// State returns the current state.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Subscribe delivers the current state first, then every change. A slow
// consumer loses intermediate states but always ends up with the latest.
// The channel is closed on Stop; cancel unsubscribes earlier.
func (s *Supervisor) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 8)
	s.mu.Lock()
	defer s.mu.Unlock()
	ch <- s.state
	if s.state.Kind == Stopped && s.started {
		close(ch)
		return ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

func (s *Supervisor) publish(st State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
	for _, ch := range s.subs {
		for {
			select {
			case ch <- st:
			default:
				select {
				case <-ch:
				default:
				}
				continue
			}
			break
		}
	}
}

// Client returns the current healthy client, or ErrNotReady / ErrDegraded /
// ErrStopped. Do not cache it: a restart replaces it.
func (s *Supervisor) Client() (*stt.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state.Kind {
	case Ready:
		if s.client != nil {
			return s.client, nil
		}
	case Degraded:
		return nil, ErrDegraded
	case Stopped:
		if s.started {
			return nil, ErrStopped
		}
	}
	return nil, ErrNotReady
}

// WaitReady blocks until the sidecar is Ready, Degraded, stopped, or ctx
// ends.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	ch, cancel := s.Subscribe()
	defer cancel()
	for {
		select {
		case st, ok := <-ch:
			if !ok {
				return ErrStopped
			}
			switch st.Kind {
			case Ready:
				return nil
			case Degraded:
				return ErrDegraded
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Session wraps stt.Session so a sidecar death surfaces as ErrInterrupted.
type Session struct {
	*stt.Session
}

func interrupted(err error) error {
	if errors.Is(err, stt.ErrSidecarExited) {
		return fmt.Errorf("%w: %w", ErrInterrupted, err)
	}
	return err
}

func (s *Session) Write(pcm []int16) error { return interrupted(s.Session.Write(pcm)) }

func (s *Session) Finish(ctx context.Context) (stt.Final, error) {
	f, err := s.Session.Finish(ctx)
	return f, interrupted(err)
}

// StartSession opens a session on the current healthy process. Sessions are
// never migrated to a new process: if the sidecar dies mid-session, Write and
// Finish return ErrInterrupted and the caller asks the user to repeat.
func (s *Supervisor) StartSession(ctx context.Context, opts ...stt.SessionOption) (*Session, error) {
	c, err := s.Client()
	if err != nil {
		return nil, err
	}
	ss, err := c.StartSession(ctx, opts...)
	if err != nil {
		return nil, interrupted(err)
	}
	return &Session{ss}, nil
}

func (s *Supervisor) finish() {
	s.publish(State{Kind: Stopped})
	s.mu.Lock()
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
	s.mu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

type failure struct {
	code  string
	fatal bool
	// free skips crash accounting and backoff (Reset).
	free bool
}

func (s *Supervisor) run(ctx context.Context) {
	defer s.finish()
	var history []time.Time
	attempt := 0
	for ctx.Err() == nil {
		f, healthyFor := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if healthyFor >= s.cfg.StableAfter {
			attempt = 0
		}
		if f.free {
			history, attempt = nil, 0
			s.publish(State{Kind: Starting})
			continue
		}
		now := time.Now()
		history = append(history, now)
		cut := 0
		for cut < len(history) && now.Sub(history[cut]) > s.cfg.CrashWindow {
			cut++
		}
		history = history[cut:]
		attempt++
		s.cfg.Logger.Warn("stt sidecar failed", "code", f.code, "attempt", attempt)

		if f.fatal || len(history) > s.cfg.MaxRestarts {
			reason := ReasonCrashLoop
			if f.fatal {
				reason = ReasonLaunchRefused
			}
			s.publish(State{Kind: Degraded, Reason: reason, LastFailure: f.code})
			select {
			case <-ctx.Done():
				return
			case <-s.reset:
				history, attempt = nil, 0
				s.publish(State{Kind: Starting})
				continue
			}
		}
		s.publish(State{Kind: Restarting, Attempt: attempt, Reason: f.code})
		t := time.NewTimer(s.cfg.Backoff(attempt))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.reset:
			t.Stop()
			history, attempt = nil, 0
		case <-t.C:
		}
	}
}

// runOnce launches one process and watches it until it fails. It returns
// how long the process was healthy.
func (s *Supervisor) runOnce(ctx context.Context) (failure, time.Duration) {
	pctx, pcancel := context.WithCancel(ctx)
	defer pcancel()

	lt := time.AfterFunc(s.cfg.LaunchTimeout, pcancel)
	c, err := s.cfg.Launcher(pctx)
	inTime := lt.Stop()
	if err == nil && !inTime {
		// The timer fired just as launch succeeded; the process context
		// is dead, so treat it as a failed launch.
		err = context.DeadlineExceeded
	}
	if err != nil {
		if c != nil {
			s.closeClient(c)
		}
		if errors.Is(err, ErrPermanent) {
			return failure{code: ReasonLaunchRefused, fatal: true}, 0
		}
		return failure{code: ReasonLaunchFailed}, 0
	}

	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
	s.publish(State{Kind: Ready})
	readyAt := time.Now()
	f := s.watch(ctx, c)
	healthy := time.Since(readyAt)

	// Drop the client before closing so Client() never hands out a dying
	// process, then make sure it is gone.
	s.mu.Lock()
	s.client = nil
	s.mu.Unlock()
	s.closeClient(c)
	return f, healthy
}

func (s *Supervisor) watch(ctx context.Context, c *stt.Client) failure {
	tick := time.NewTicker(s.cfg.HealthInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return failure{code: ReasonReset}
		case <-s.reset:
			return failure{code: ReasonReset, free: true}
		case <-c.Done():
			return failure{code: ReasonSidecarExited}
		case <-tick.C:
			hctx, cancel := context.WithTimeout(ctx, s.cfg.HealthTimeout)
			h, err := c.Health(hctx)
			cancel()
			switch {
			case ctx.Err() != nil:
				return failure{code: ReasonReset}
			case err == nil && h.OK && h.Initialized:
			case errors.Is(err, stt.ErrSidecarExited):
				return failure{code: ReasonSidecarExited}
			case errors.Is(err, context.DeadlineExceeded):
				return failure{code: ReasonHealthTimeout}
			default:
				return failure{code: ReasonHealthFailed}
			}
		}
	}
}

// closeClient ends a process politely and kills it when it does not
// cooperate within StopTimeout. It always returns with the client done.
func (s *Supervisor) closeClient(c *stt.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StopTimeout)
	defer cancel()
	_ = c.Close(ctx)
}
