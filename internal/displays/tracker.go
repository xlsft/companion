// Package displays keeps the current display topology, versions it and tells
// callers when a capture no longer matches the screen (FR-SCREEN-005).
package displays

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Backend is the OS-specific source of display facts. It only reports raw
// displays; versioning, debouncing and fan-out belong to the Tracker.
type Backend interface {
	// Displays returns the current outputs. After Changed is closed it
	// returns the error that ended the backend.
	Displays() ([]contracts.Display, error)
	// Changed receives a value whenever something may have changed (events
	// may be coalesced by the backend). It is closed when the backend dies.
	Changed() <-chan struct{}
	Close() error
}

// DefaultDebounce lets a replug (several outputs and modes arriving as a
// burst of events) settle into a single version bump.
const DefaultDebounce = 100 * time.Millisecond

// maxDebounce bounds how long a never-ending event stream can defer a bump.
const maxDebounce = time.Second

// Tracker implements contracts.DisplayService on top of a Backend.
type Tracker struct {
	backend  Backend
	debounce time.Duration
	cancel   context.CancelFunc
	done     chan struct{}

	mu      sync.Mutex
	topo    contracts.Topology
	pending bool // events seen but not yet folded into topo
	err     error
	subs    map[chan contracts.Topology]struct{}
}

var _ contracts.DisplayService = (*Tracker)(nil)

// NewTracker reads the initial topology (version 1) and follows the backend
// until ctx is done or Close is called. A zero debounce selects
// DefaultDebounce. The Tracker owns the backend and closes it.
func NewTracker(ctx context.Context, b Backend, debounce time.Duration) (*Tracker, error) {
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	ds, err := b.Displays()
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &Tracker{
		backend:  b,
		debounce: debounce,
		cancel:   cancel,
		done:     make(chan struct{}),
		topo:     contracts.Topology{Version: 1, Displays: normalize(ds)},
		subs:     map[chan contracts.Topology]struct{}{},
	}
	go t.run(ctx)
	return t, nil
}

// Close stops following the backend, closes it and ends all Watch channels.
func (t *Tracker) Close() error {
	t.cancel()
	<-t.done
	return t.backend.Close()
}

func (t *Tracker) run(ctx context.Context) {
	defer close(t.done)
	defer t.closeSubs()
	changed := t.backend.Changed()
	var timer *time.Timer
	var timerC <-chan time.Time
	var first time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case _, ok := <-changed:
			if !ok {
				t.fail()
				return
			}
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			t.mu.Lock()
			t.pending = true
			t.mu.Unlock()
			wait := min(t.debounce, max(0, maxDebounce-now.Sub(first)))
			if timer == nil {
				timer = time.NewTimer(wait)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(wait)
			}
		case <-timerC:
			timer, timerC, first = nil, nil, time.Time{}
			t.refresh()
		}
	}
}

// fail records why the backend died; from then on nothing can be trusted.
func (t *Tracker) fail() {
	_, err := t.backend.Displays()
	if err == nil {
		err = errors.New("display backend stopped")
	}
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
}

func (t *Tracker) refresh() {
	ds, err := t.backend.Displays()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = false
	if err != nil {
		t.err = err
		return
	}
	ds = normalize(ds)
	if slices.Equal(ds, t.topo.Displays) {
		return
	}
	t.topo = contracts.Topology{Version: t.topo.Version + 1, Displays: ds}
	for ch := range t.subs {
		// Keep-latest: the publisher is the only sender, so after dropping
		// a stale value the send cannot block.
		select {
		case <-ch:
		default:
		}
		ch <- t.snapshot()
	}
}

// snapshot copies the topology; the caller holds t.mu.
func (t *Tracker) snapshot() contracts.Topology {
	return contracts.Topology{Version: t.topo.Version, Displays: slices.Clone(t.topo.Displays)}
}

func (t *Tracker) closeSubs() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ch := range t.subs {
		close(ch)
	}
	clear(t.subs)
}

// Topology returns the current snapshot.
func (t *Tracker) Topology(context.Context) (contracts.Topology, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return contracts.Topology{}, t.err
	}
	return t.snapshot(), nil
}

// Watch delivers the topology after each change. A subscriber that does not
// keep up only ever sees the latest value; it never blocks the others.
func (t *Tracker) Watch(ctx context.Context) (<-chan contracts.Topology, error) {
	ch := make(chan contracts.Topology, 1)
	t.mu.Lock()
	if t.err != nil {
		err := t.err
		t.mu.Unlock()
		return nil, err
	}
	select {
	case <-t.done:
		t.mu.Unlock()
		return nil, errors.New("displays: tracker closed")
	default:
	}
	t.subs[ch] = struct{}{}
	t.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-t.done:
			return // closeSubs ends the channel
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, ok := t.subs[ch]; ok {
			delete(t.subs, ch)
			close(ch)
		}
	}()
	return ch, nil
}

// IsStale reports whether meta was captured under a topology that is no
// longer current. While backend events are still being debounced the answer
// is conservatively true.
func (t *Tracker) IsStale(meta contracts.CaptureMetadata) bool {
	return t.RequireFresh(meta) != nil
}

// RequireFresh returns an error wrapping contracts.ErrStale when meta must
// not be acted upon. Computer use must refuse; passive hints must recapture.
func (t *Tracker) RequireFresh(meta contracts.CaptureMetadata) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return staleErr("display state unknown")
	}
	if meta.DisplayID != "" {
		if _, ok := t.topo.Display(meta.DisplayID); !ok {
			return staleErr("display removed")
		}
	}
	if meta.TopologyVersion != t.topo.Version || t.pending {
		return staleErr("topology changed")
	}
	return nil
}

func staleErr(reason string) error {
	return fmt.Errorf("%w: %s", contracts.ErrStale, reason)
}

// normalize sorts by ID so backends need not agree on ordering and diffs
// stay stable.
func normalize(in []contracts.Display) []contracts.Display {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b contracts.Display) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return out
}
