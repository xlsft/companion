package restraint

import (
	"context"
	"sync"
	"time"
)

type Kind int

const (
	// UserInitiated actions (the user pressed Talk) are never suppressed.
	UserInitiated Kind = iota
	// Spontaneous items (agent finished, routine result, suggestion).
	Spontaneous
)

type Decision int

const (
	Deliver Decision = iota
	Defer
)

func (d Decision) String() string {
	if d == Defer {
		return "defer"
	}
	return "deliver"
}

// Item is a deferred notification. Items with the same non-empty Key collapse
// into the latest one.
type Item struct {
	Key     string
	Payload any
}

// StateSource is satisfied by *Monitor.
type StateSource interface {
	State() State
	Subscribe() (<-chan State, func())
}

const DefaultMaxQueue = 20

const ReasonDND = "dnd"

type Policy struct {
	src      StateSource
	now      func() time.Time
	maxQueue int
	wake     chan struct{} // nudges Watch when the DND deadline changes

	mu       sync.Mutex
	enabled  bool
	dndUntil time.Time
	queue    []Item
	dropped  int
	flush    func([]Item)
}

type PolicyOption func(*Policy)

func WithClock(now func() time.Time) PolicyOption { return func(p *Policy) { p.now = now } }
func WithMaxQueue(n int) PolicyOption             { return func(p *Policy) { p.maxQueue = n } }

// NewPolicy returns an enabled policy (restraint defaults to on). src may be
// nil, in which case only the DND override restrains.
func NewPolicy(src StateSource, opts ...PolicyOption) *Policy {
	p := &Policy{src: src, now: time.Now, maxQueue: DefaultMaxQueue, enabled: true, wake: make(chan struct{}, 1)}
	for _, o := range opts {
		o(p)
	}
	if p.maxQueue <= 0 {
		p.maxQueue = DefaultMaxQueue
	}
	return p
}

// SetFlush installs the hook that receives queued items once restraint ends.
// It is called outside the policy lock.
func (p *Policy) SetFlush(f func([]Item)) {
	p.mu.Lock()
	p.flush = f
	p.mu.Unlock()
}

// SetEnabled is the settings flag. Disabling releases the queue.
func (p *Policy) SetEnabled(on bool) {
	p.mu.Lock()
	p.enabled = on
	p.mu.Unlock()
	p.Reevaluate()
}

// DoNotDisturbUntil silences spontaneous items until t; a zero or past t
// clears the override. It applies even when automatic restraint is disabled,
// because the user asked for it explicitly.
func (p *Policy) DoNotDisturbUntil(t time.Time) {
	p.mu.Lock()
	p.dndUntil = t
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	p.Reevaluate()
}

// DNDUntil returns the active override end, or the zero time.
func (p *Policy) DNDUntil() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dndUntil.After(p.now()) {
		return time.Time{}
	}
	return p.dndUntil
}

// State merges the monitor state, the enabled flag and the DND override.
func (p *Policy) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked()
}

func (p *Policy) stateLocked() State {
	var st State
	if p.enabled && p.src != nil {
		st = p.src.State()
		st.Reasons = append([]string(nil), st.Reasons...)
	}
	if p.dndUntil.After(p.now()) {
		st.Restrained = true
		st.Reasons = append(st.Reasons, ReasonDND)
	}
	return st
}

func (p *Policy) Allow(kind Kind) Decision {
	if kind == UserInitiated {
		return Deliver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stateLocked().Restrained {
		return Defer
	}
	return Deliver
}

// Submit decides for it: on Deliver the caller shows it now, on Defer the
// policy has queued it and will hand it to the flush hook later.
func (p *Policy) Submit(kind Kind, it Item) Decision {
	if kind == UserInitiated {
		return Deliver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stateLocked().Restrained {
		return Deliver
	}
	if it.Key != "" {
		for i, q := range p.queue {
			if q.Key == it.Key {
				p.queue = append(p.queue[:i], p.queue[i+1:]...)
				break
			}
		}
	}
	p.queue = append(p.queue, it)
	for len(p.queue) > p.maxQueue {
		p.queue = p.queue[1:] // oldest is the least relevant
		p.dropped++
	}
	return Defer
}

func (p *Policy) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Dropped counts items lost to the queue cap.
func (p *Policy) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

// Reevaluate flushes the queue if restraint has ended. Call it after anything
// that may have lifted restraint (Watch does so for monitor changes and DND expiry).
func (p *Policy) Reevaluate() {
	p.mu.Lock()
	if p.stateLocked().Restrained || len(p.queue) == 0 {
		p.mu.Unlock()
		return
	}
	items := p.queue
	p.queue = nil
	f := p.flush
	p.mu.Unlock()
	if f != nil {
		f(items)
	}
}

// Watch re-evaluates on monitor changes and at DND expiry until ctx is done.
func (p *Policy) Watch(ctx context.Context) {
	var ch <-chan State
	if p.src != nil {
		c, cancel := p.src.Subscribe()
		defer cancel()
		ch = c
	}
	for {
		var expiry <-chan time.Time
		var timer *time.Timer
		if until := p.DNDUntil(); !until.IsZero() {
			timer = time.NewTimer(until.Sub(p.now()) + time.Millisecond)
			expiry = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ch:
		case <-p.wake:
		case <-expiry:
		}
		if timer != nil {
			timer.Stop()
		}
		p.Reevaluate()
	}
}
