package walkthrough

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Presenter shows a step on screen and speaks it; internal/talk implements
// it over the overlay, the screen capture, the model and the speaker.
type Presenter interface {
	// Show draws the hints of p.Step. With p.Capture set they are drawn as
	// planned on that screenshot; otherwise a new screenshot is taken and
	// the model may mark the step anew and revise it. The returned step is
	// what is shown and said.
	Show(ctx context.Context, p Presentation) (Shown, error)
	// Check asks the model, on a new screenshot, whether the user has done
	// p.Step, after the screen around it changed (auto-advance).
	Check(ctx context.Context, p Presentation) (Verdict, error)
	// Say speaks text and returns when it was spoken.
	Say(ctx context.Context, text string) error
	// End removes every hint drawn for ref's walkthrough.
	End(ctx context.Context, ref contracts.SessionRef)
}

// Presentation is one showing of a step.
type Presentation struct {
	// Ref scopes the hints on the overlay: every showing has a new
	// generation, so it replaces the hints of the one before.
	Ref          contracts.SessionRef
	Goal         string
	Index, Total int
	Step         Step
	// Capture is the screenshot Step.Shapes are normalized to; "" asks for
	// a new one.
	Capture contracts.CaptureID
	// Brief is the whole walkthrough for the model (View.Brief).
	Brief string
}

// Shown is a step as Show put it on screen.
type Shown struct {
	Step Step
	// Area holds the hints drawn, in desktop coordinates; zero for none.
	Area contracts.Rect
}

// Verdict is the model's answer to a Check.
type Verdict struct {
	// Control moves the walkthrough, typically on to the next step; nil
	// when the step is not done yet.
	Control *Control
	// Step is the step revised; zero when unchanged.
	Step Step
	// Area holds hints drawn anew, in desktop coordinates; zero for none.
	Area contracts.Rect
}

// Watch lets a running step watch the screen for the user doing it.
type Watch struct {
	Capturer contracts.ScreenCapturer
	// Target is what to sample for a step without hints.
	Target func() contracts.CaptureRequest
	// Enabled is asked whenever a step is shown; nil means on.
	Enabled func() bool
	// Interval overrides WatchInterval (tests).
	Interval time.Duration
}

type Config struct {
	Presenter Presenter
	// Store keeps the progress across restarts; nil keeps it in memory.
	Store Store
	// Watch enables auto-advance; nil leaves moving on to the user.
	Watch *Watch
	Log   *slog.Logger
	Now   func() time.Time
	NewID func() string
}

const storeTimeout = 5 * time.Second

// Service runs the walkthrough in progress. Commands come from voice
// intents and the chat card alike; each showing of a step runs with its
// own context, which the next command, Hush or Stop cancels.
type Service struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// pmu orders writes to the Store.
	pmu sync.Mutex

	mu      sync.Mutex
	closed  bool
	st      State
	capture contracts.CaptureID // screenshot the plan was made on
	gen     uint64
	stop    context.CancelFunc // of the showing in flight
	// preparing: the current showing is working out its hints.
	preparing bool
	// watch is the current step's screen watcher; wgen tells its
	// successive runs apart.
	watchStop context.CancelFunc
	wgen      uint64
	subs      map[chan View]struct{}
}

func New(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	s := &Service{cfg: cfg, subs: map[chan View]struct{}{}, st: State{Status: StatusIdle}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Restore loads a walkthrough left unfinished by the last run. It comes
// back paused: nothing is spoken until the user resumes it.
func (s *Service) Restore(ctx context.Context) error {
	if s.cfg.Store == nil {
		return nil
	}
	st, ok, err := s.cfg.Store.Load(ctx)
	if err != nil || !ok {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.st.Status.Active() {
		return nil
	}
	st.Status, st.Fresh = StatusPaused, false
	s.st = st
	s.publishLocked()
	return nil
}

// Start replaces any walkthrough in progress with plan and shows its first
// step. capture is the screenshot the plan's shapes refer to, "" if none.
func (s *Service) Start(ctx context.Context, plan Plan, capture contracts.CaptureID) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return context.Canceled
	}
	s.endLocked()
	now := s.cfg.Now()
	s.st = State{ID: s.cfg.NewID(), Plan: plan, Status: StatusRunning, Fresh: capture != "", Started: now, Updated: now}
	s.capture = capture
	s.presentLocked()
	s.mu.Unlock()
	s.persist()
	return nil
}

// Do applies a command; ErrNotActive when no walkthrough is in progress.
func (s *Service) Do(_ context.Context, cmd Command) error {
	return s.move(func(st State) (State, Effect, error) { return st.Apply(cmd) }, nil)
}

// ErrMoved: the walkthrough is no longer at the step a decision was made
// on, e.g. auto-advance moved it while the model was answering.
var ErrMoved = errors.New("walkthrough moved on meanwhile")

// Control applies the model's walkthrough_control decision, made while the
// walkthrough was at step at (0-based); ErrMoved if it is not there now.
func (s *Service) Control(_ context.Context, c Control, at int) error {
	return s.move(func(st State) (State, Effect, error) {
		if c.Action == CmdGoto {
			return st.GoTo(c.Step - 1)
		}
		return st.Apply(c.Action)
	}, func(st State) bool { return st.Index == at })
}

// Revise changes the current step's title and instruction.
func (s *Service) Revise(_ context.Context, r Revision) error {
	if err := (Plan{Goal: "-", Steps: []Step{{Title: r.Title, Say: r.Say}}}).Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || !s.st.Status.Active() {
		s.mu.Unlock()
		return ErrNotActive
	}
	s.reviseLocked(s.st.Index, Step{Title: r.Title, Say: r.Say})
	s.publishLocked()
	s.mu.Unlock()
	s.persist()
	return nil
}

// move applies a transition if ok (nil: always) accepts the state.
func (s *Service) move(apply func(State) (State, Effect, error), ok func(State) bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrNotActive
	}
	if ok != nil && s.st.Status.Active() && !ok(s.st) {
		s.mu.Unlock()
		return ErrMoved
	}
	next, eff, err := apply(s.st)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	next.Updated = s.cfg.Now()
	switch eff {
	case EffectPresent:
		s.st = next
		s.presentLocked()
	case EffectHush:
		s.st = next
		s.hushLocked()
		s.disarmLocked()
	case EffectEnd:
		s.endLocked()
		s.st = next
	default:
		s.st = next
	}
	s.publishLocked()
	s.mu.Unlock()
	s.persist()
	return nil
}

// Stop ends the walkthrough in progress, if any, and removes its hints.
func (s *Service) Stop(ctx context.Context) {
	if err := s.Do(ctx, CmdStop); err != nil && !errors.Is(err, ErrNotActive) {
		s.cfg.Log.Warn("walkthrough stop", "err", err)
	}
}

// Hush stops what the current step is saying, e.g. because the user
// started talking; the walkthrough and its hints stay.
func (s *Service) Hush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return
	}
	s.hushLocked()
	s.publishLocked()
}

// Active reports whether a walkthrough is running or paused.
func (s *Service) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Status.Active()
}

// View is the walkthrough as the chat card shows it.
func (s *Service) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked()
}

// Subscribe returns the current view followed by every change; a slow
// reader only misses views that were already replaced.
func (s *Service) Subscribe() (<-chan View, func()) {
	ch := make(chan View, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(ch)
		return ch, func() {}
	}
	s.subs[ch] = struct{}{}
	ch <- s.viewLocked()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Close cancels the showing in flight and waits for it; the progress
// stays stored for the next run.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for ch := range s.subs {
		close(ch)
		delete(s.subs, ch)
	}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

func (s *Service) refLocked() contracts.SessionRef {
	return contracts.SessionRef{ID: contracts.SessionID("walkthrough-" + s.st.ID), Generation: s.gen}
}

func (s *Service) presentationLocked() Presentation {
	st := s.st
	return Presentation{Ref: s.refLocked(), Goal: st.Plan.Goal, Index: st.Index, Total: len(st.Plan.Steps),
		Step: st.Plan.Steps[st.Index], Brief: s.viewLocked().Brief()}
}

// presentLocked starts showing the current step.
func (s *Service) presentLocked() {
	s.hushLocked()
	s.disarmLocked()
	s.gen++
	ctx, cancel := context.WithCancel(s.ctx)
	s.stop = cancel
	st := s.st
	p := s.presentationLocked()
	if st.Fresh {
		p.Capture = s.capture
	}
	// The plan's screenshot is good for one showing of the first step.
	s.st.Fresh = false
	s.preparing = p.Capture == ""
	gen := s.gen
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		shown, err := s.cfg.Presenter.Show(ctx, p)
		s.mu.Lock()
		if s.gen == gen && s.st.Status == StatusRunning {
			// Watched from now on, also when Hush cut the showing short:
			// the user may well act while being talked to.
			s.armLocked(shown.Area)
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		step := shown.Step
		if err != nil {
			// The step is still said; only its hints are missing.
			s.cfg.Log.Warn("walkthrough step hints", "err", err)
			step = p.Step
		}
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			return
		}
		s.preparing = false
		// The showing's text applies only to the step it started from: a
		// Revise that landed meanwhile is newer and must not be undone.
		revised := false
		if cur := s.st.Plan.Steps[p.Index]; cur.Title == p.Step.Title && cur.Say == p.Step.Say {
			revised = s.reviseLocked(p.Index, step)
		}
		step = s.st.Plan.Steps[p.Index]
		s.publishLocked()
		s.mu.Unlock()
		if revised {
			s.persist()
		}
		if err := s.cfg.Presenter.Say(ctx, step.Say); err != nil && ctx.Err() == nil {
			s.cfg.Log.Warn("walkthrough step speech", "err", err)
		}
	}()
}

// reviseLocked replaces step i's text with step's, if that is a change.
func (s *Service) reviseLocked(i int, step Step) bool {
	cur := s.st.Plan.Steps[i]
	if step.Title == "" || step.Say == "" || (step.Title == cur.Title && step.Say == cur.Say) {
		return false
	}
	steps := slices.Clone(s.st.Plan.Steps)
	steps[i].Title, steps[i].Say = step.Title, step.Say
	s.st.Plan.Steps = steps
	return true
}

// hushLocked cancels the showing in flight.
func (s *Service) hushLocked() {
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	s.preparing = false
}

// endLocked stops the current walkthrough's showing and removes its hints.
func (s *Service) endLocked() {
	s.hushLocked()
	s.disarmLocked()
	if s.st.Status.Active() && s.cfg.Presenter != nil {
		// Generation 0 up to the current one: every showing's hints.
		s.cfg.Presenter.End(context.WithoutCancel(s.ctx), s.refLocked())
	}
}

func (s *Service) viewLocked() View {
	v := s.st.view()
	v.Preparing = s.preparing
	v.Watching = s.watchStop != nil && s.st.Status == StatusRunning
	return v
}

func (s *Service) publishLocked() {
	v := s.viewLocked()
	for ch := range s.subs {
		select {
		case <-ch:
		default:
		}
		ch <- v
	}
}

// persist stores the latest progress, or forgets it once the walkthrough
// has ended.
func (s *Service) persist() {
	if s.cfg.Store == nil {
		return
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	s.mu.Lock()
	st := s.st
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	var err error
	if st.Status.Active() {
		err = s.cfg.Store.Save(ctx, st)
	} else {
		err = s.cfg.Store.Delete(ctx)
	}
	if err != nil {
		s.cfg.Log.Warn("walkthrough progress not stored", "err", err)
	}
}
