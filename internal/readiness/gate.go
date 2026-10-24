// Package readiness is the setup-only Readiness Gate (OC-70, US-00,
// FR-ONB-001…014). Until every mandatory check has passed the app is not
// Ready: only the setup wizard exists, and no hotkey, tray action, overlay
// or agent runtime may be started. The gate is the single source of truth
// for that decision.
package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// State of the wizard (FR-ONB-010).
type State string

const (
	SetupRequired State = "setup_required"
	Configuring   State = "configuring"
	Validating    State = "validating"
	Ready         State = "ready"
)

// Step is one wizard step, in order (FR-ONB-001).
type Step string

const (
	StepPermissions Step = "permissions"
	StepBYOK        Step = "byok"
	StepSTT         Step = "stt"
	StepTTS         Step = "tts"
	StepPlatform    Step = "platform"
	StepEndToEnd    Step = "end_to_end"
)

// Steps is the fixed wizard order. There is no skip (FR-ONB-004).
var Steps = []Step{StepPermissions, StepBYOK, StepSTT, StepTTS, StepPlatform, StepEndToEnd}

// CheckStatus is the result of one step's check.
type CheckStatus string

const (
	Pending CheckStatus = "pending"
	Passed  CheckStatus = "passed"
	Failed  CheckStatus = "failed"
)

// Result is a step's last check outcome. Detail items are technical codes
// shown with a fix action (FR-ONB-007), never user content.
type Result struct {
	Status  CheckStatus       `json:"status"`
	At      time.Time         `json:"at,omitempty"`
	Code    string            `json:"code,omitempty"`
	Detail  map[string]string `json:"detail,omitempty"`
	Version int               `json:"version,omitempty"` // config version the check ran against
}

// Progress is the persisted wizard state (FR-ONB-011).
type Progress struct {
	State   State           `json:"state"`
	Current Step            `json:"current"`
	Results map[Step]Result `json:"results"`
	// ReadyAt is set once, atomically with State=Ready.
	ReadyAt time.Time `json:"ready_at,omitempty"`
}

// Store persists Progress; implemented over the settings KV in the app.
type Store interface {
	Load(ctx context.Context) ([]byte, error) // nil, nil when absent
	Save(ctx context.Context, data []byte) error
}

// Check runs the fixed test for a step (FR-ONB-005/008: real system calls,
// fixed requests and phrases, never free user input).
type Check func(ctx context.Context) Result

var (
	ErrNotReady   = errors.New("readiness: app is not ready")
	ErrStepLocked = errors.New("readiness: previous steps have not passed")
	ErrUnknown    = errors.New("readiness: unknown step")
)

// Gate owns the readiness decision.
type Gate struct {
	store  Store
	checks map[Step]Check
	now    func() time.Time

	mu    sync.Mutex
	p     Progress
	subs  map[chan Progress]struct{}
	ready chan struct{} // closed on the transition to Ready
}

// Open loads persisted progress. A Ready app stays Ready across restarts
// without re-running the setup (acceptance of FR-ONB criteria).
func Open(ctx context.Context, store Store, checks map[Step]Check) (*Gate, error) {
	g := &Gate{store: store, checks: checks, now: time.Now, subs: map[chan Progress]struct{}{}, ready: make(chan struct{})}
	data, err := store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("readiness: load progress: %w", err)
	}
	g.p = Progress{State: SetupRequired, Current: Steps[0], Results: map[Step]Result{}}
	if len(data) > 0 {
		var p Progress
		if err := json.Unmarshal(data, &p); err != nil {
			// A corrupt record must not unlock the app; start over.
			return g, nil
		}
		if p.Results == nil {
			p.Results = map[Step]Result{}
		}
		if !slices.Contains(Steps, p.Current) {
			p.Current = Steps[0]
		}
		g.p = p
		g.p.Current = g.firstUnpassed()
		if g.p.State == Ready && !g.allPassed() {
			g.p.State = Configuring
		}
	}
	if g.p.State == Ready {
		close(g.ready)
	}
	return g, nil
}

// Progress returns a copy of the current state.
func (g *Gate) Progress() Progress {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.copyLocked()
}

// IsReady reports whether the main UI and global interaction may exist.
func (g *Gate) IsReady() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.p.State == Ready
}

// Require returns ErrNotReady unless Ready. Every entry point that could
// start a conversation (hotkey, tray, deep link, IPC) calls it
// (FR-ONB-003, OC-76).
func (g *Gate) Require() error {
	if !g.IsReady() {
		return ErrNotReady
	}
	return nil
}

// ReadyCh is closed when the app becomes Ready, so the app builds the main
// UI and registers hotkeys exactly once (FR-ONB-002).
func (g *Gate) ReadyCh() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ready
}

// Subscribe delivers every progress change; the latest value wins for slow
// readers.
func (g *Gate) Subscribe() (<-chan Progress, func()) {
	ch := make(chan Progress, 1)
	g.mu.Lock()
	g.subs[ch] = struct{}{}
	ch <- g.copyLocked()
	g.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.subs, ch)
			g.mu.Unlock()
		})
	}
}

// Run executes the check of step. Steps unlock in order: a later step can
// only run once all earlier ones passed. A failure keeps the wizard on that
// step (FR-ONB-007). Running the end-to-end step successfully, with all
// other steps passed, is the only way to reach Ready.
func (g *Gate) Run(ctx context.Context, step Step) (Result, error) {
	check, ok := g.checks[step]
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknown, step)
	}
	g.mu.Lock()
	if g.p.State == Ready {
		g.mu.Unlock()
		return g.p.Results[step], nil
	}
	idx := slices.Index(Steps, step)
	for _, prev := range Steps[:idx] {
		if g.p.Results[prev].Status != Passed {
			g.mu.Unlock()
			return Result{}, fmt.Errorf("%w: %s", ErrStepLocked, prev)
		}
	}
	g.p.State = Validating
	g.p.Current = step
	g.publishLocked()
	g.mu.Unlock()

	res := check(ctx)
	if res.Status == "" {
		res.Status = Failed
	}
	if ctx.Err() != nil && res.Status != Passed {
		res = Result{Status: Pending}
	}
	if res.At.IsZero() {
		res.At = g.now()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.p.Results[step] = res
	if res.Status == Passed {
		// Any earlier step re-run invalidates the end-to-end proof.
		if step != StepEndToEnd {
			delete(g.p.Results, StepEndToEnd)
		}
	}
	g.p.Current = g.firstUnpassed()
	g.p.State = Configuring
	if step == StepEndToEnd && res.Status == Passed && g.allPassed() {
		g.p.State = Ready
		g.p.ReadyAt = g.now()
	}
	if err := g.saveLocked(ctx); err != nil {
		// Without a durable record the transition did not happen.
		if g.p.State == Ready {
			g.p.State = Configuring
			g.p.ReadyAt = time.Time{}
		}
		g.publishLocked()
		return res, err
	}
	if g.p.State == Ready {
		close(g.ready)
	}
	g.publishLocked()
	return res, nil
}

// Select moves the wizard to a step for display (back navigation); it does
// not change results.
func (g *Gate) Select(ctx context.Context, step Step) error {
	if !slices.Contains(Steps, step) {
		return fmt.Errorf("%w: %s", ErrUnknown, step)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.p.State == Ready {
		return nil
	}
	g.p.Current = step
	if g.p.State == SetupRequired {
		g.p.State = Configuring
	}
	g.publishLocked()
	return g.saveLocked(ctx)
}

// Revoke returns the app to setup, e.g. after a configuration reset or when
// a mandatory component (the STT model, the only provider) was removed
// (FR-ONB-012). Transient network or provider failures must not call it.
// The caller restarts the process into setup mode, since main UI and
// hotkeys exist only after Ready.
func (g *Gate) Revoke(ctx context.Context, steps ...Step) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(steps) == 0 {
		steps = Steps
	}
	for _, s := range steps {
		delete(g.p.Results, s)
	}
	delete(g.p.Results, StepEndToEnd)
	g.p.State = Configuring
	if len(g.p.Results) == 0 {
		g.p.State = SetupRequired
	}
	g.p.ReadyAt = time.Time{}
	g.p.Current = g.firstUnpassed()
	g.ready = make(chan struct{})
	g.publishLocked()
	return g.saveLocked(ctx)
}

func (g *Gate) firstUnpassed() Step {
	for _, s := range Steps {
		if g.p.Results[s].Status != Passed {
			return s
		}
	}
	return StepEndToEnd
}

func (g *Gate) allPassed() bool {
	for _, s := range Steps {
		if g.p.Results[s].Status != Passed {
			return false
		}
	}
	return true
}

func (g *Gate) saveLocked(ctx context.Context) error {
	data, err := json.Marshal(g.p)
	if err != nil {
		return err
	}
	return g.store.Save(ctx, data)
}

func (g *Gate) copyLocked() Progress {
	p := g.p
	p.Results = make(map[Step]Result, len(g.p.Results))
	for k, v := range g.p.Results {
		p.Results[k] = v
	}
	return p
}

func (g *Gate) publishLocked() {
	p := g.copyLocked()
	for ch := range g.subs {
		select {
		case <-ch:
		default:
		}
		ch <- p
	}
}
