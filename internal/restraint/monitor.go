// Package restraint decides when spontaneous notifications must be held back
// (active call, presentation, screen sharing; FR-UI-006, FR-OVERLAY-010).
// Everything here is pure Go; OS probes live in restraint/linux etc.
package restraint

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Signal is one source of "the user should not be interrupted".
// An error means "could not tell": it counts as inactive and is kept for
// diagnostics, never as restraint.
type Signal interface {
	Name() string
	Active(ctx context.Context) (bool, error)
}

type State struct {
	Restrained bool
	Reasons    []string // sorted signal names (plus "dnd" from Policy)
}

func (s State) equal(o State) bool {
	if s.Restrained != o.Restrained || len(s.Reasons) != len(o.Reasons) {
		return false
	}
	for i := range s.Reasons {
		if s.Reasons[i] != o.Reasons[i] {
			return false
		}
	}
	return true
}

const (
	DefaultInterval     = 5 * time.Second // idle CPU budget (NFR-002): no tight polling
	DefaultProbeTimeout = 3 * time.Second
)

type Monitor struct {
	signals  []Signal
	interval time.Duration
	timeout  time.Duration

	mu     sync.Mutex
	active map[string]bool   // polled results
	pushed map[string]bool   // push overrides until the next poll of that signal
	diag   map[string]string // signal -> last probe failure
	state  State
	subs   map[int]chan State
	nextID int
}

type Option func(*Monitor)

func WithInterval(d time.Duration) Option     { return func(m *Monitor) { m.interval = d } }
func WithProbeTimeout(d time.Duration) Option { return func(m *Monitor) { m.timeout = d } }

func NewMonitor(signals []Signal, opts ...Option) *Monitor {
	m := &Monitor{
		signals:  signals,
		interval: DefaultInterval,
		timeout:  DefaultProbeTimeout,
		active:   map[string]bool{},
		pushed:   map[string]bool{},
		diag:     map[string]string{},
		subs:     map[int]chan State{},
	}
	for _, o := range opts {
		o(m)
	}
	if m.interval <= 0 {
		m.interval = DefaultInterval
	}
	if m.timeout <= 0 {
		m.timeout = DefaultProbeTimeout
	}
	return m
}

// Run polls until ctx is done. The first poll happens immediately.
func (m *Monitor) Run(ctx context.Context) {
	m.Poll(ctx)
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Poll(ctx)
		}
	}
}

// Poll probes every signal once, in parallel, each bounded by the probe timeout.
func (m *Monitor) Poll(ctx context.Context) {
	type res struct {
		name   string
		active bool
		err    error
	}
	out := make([]res, len(m.signals))
	var wg sync.WaitGroup
	for i, s := range m.signals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()
			a, err := s.Active(pctx)
			out[i] = res{s.Name(), a && err == nil, err}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	for _, r := range out {
		m.active[r.name] = r.active
		delete(m.pushed, r.name)
		if r.err != nil {
			m.diag[r.name] = r.err.Error()
		} else {
			delete(m.diag, r.name)
		}
	}
	m.recompute()
	m.mu.Unlock()
}

// Push sets a signal from an event source (e.g. a D-Bus signal) without
// waiting for the next poll. The next poll of the same signal supersedes it.
func (m *Monitor) Push(name string, active bool) {
	m.mu.Lock()
	m.pushed[name] = active
	m.recompute()
	m.mu.Unlock()
}

func (m *Monitor) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Diagnostics returns the last failure reason per signal (empty when healthy).
func (m *Monitor) Diagnostics() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := make(map[string]string, len(m.diag))
	for k, v := range m.diag {
		d[k] = v
	}
	return d
}

// Subscribe returns a channel that receives the state on every change. Slow
// consumers only ever see the latest state.
func (m *Monitor) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 1)
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	m.subs[id] = ch
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

// recompute must be called with mu held.
func (m *Monitor) recompute() {
	set := map[string]bool{}
	for k, v := range m.active {
		if v {
			set[k] = true
		}
	}
	for k, v := range m.pushed {
		if v {
			set[k] = true
		} else {
			delete(set, k)
		}
	}
	reasons := make([]string, 0, len(set))
	for k := range set {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	next := State{Restrained: len(reasons) > 0, Reasons: reasons}
	if next.equal(m.state) {
		return
	}
	m.state = next
	for _, ch := range m.subs {
		select {
		case <-ch:
		default:
		}
		ch <- next
	}
}
