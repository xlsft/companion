package restraint

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSignal struct {
	name   string
	active atomic.Bool
	err    atomic.Value // error
	calls  atomic.Int32
	block  bool
}

func (f *fakeSignal) Name() string { return f.name }
func (f *fakeSignal) Active(ctx context.Context) (bool, error) {
	f.calls.Add(1)
	if f.block {
		<-ctx.Done()
		return true, ctx.Err()
	}
	if e, _ := f.err.Load().(error); e != nil {
		return true, e
	}
	return f.active.Load(), nil
}

type fakeSource struct {
	mu sync.Mutex
	st State
}

func (f *fakeSource) State() State { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *fakeSource) Subscribe() (<-chan State, func()) {
	return make(chan State), func() {}
}
func (f *fakeSource) set(r bool, reasons ...string) {
	f.mu.Lock()
	f.st = State{Restrained: r, Reasons: reasons}
	f.mu.Unlock()
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestMonitorPoll(t *testing.T) {
	a := &fakeSignal{name: "call"}
	b := &fakeSignal{name: "screen"}
	m := NewMonitor([]Signal{a, b})
	ctx := context.Background()
	m.Poll(ctx)
	if m.State().Restrained {
		t.Fatal("restrained with nothing active")
	}
	b.active.Store(true)
	a.active.Store(true)
	m.Poll(ctx)
	got := m.State()
	if !got.Restrained || fmt.Sprint(got.Reasons) != "[call screen]" {
		t.Fatalf("state %+v", got)
	}
	a.active.Store(false)
	b.err.Store(errors.New("pw-dump missing"))
	m.Poll(ctx)
	if m.State().Restrained {
		t.Fatalf("probe error must count as inactive: %+v", m.State())
	}
	if m.Diagnostics()["screen"] != "pw-dump missing" {
		t.Fatalf("diag %v", m.Diagnostics())
	}
}

func TestMonitorProbeTimeout(t *testing.T) {
	s := &fakeSignal{name: "slow", block: true}
	m := NewMonitor([]Signal{s}, WithProbeTimeout(20*time.Millisecond))
	start := time.Now()
	m.Poll(context.Background())
	if time.Since(start) > 2*time.Second {
		t.Fatal("poll did not honour timeout")
	}
	if m.State().Restrained || m.Diagnostics()["slow"] == "" {
		t.Fatalf("state %+v diag %v", m.State(), m.Diagnostics())
	}
}

func TestMonitorRunSubscribePush(t *testing.T) {
	s := &fakeSignal{name: "call"}
	m := NewMonitor([]Signal{s}, WithInterval(10*time.Millisecond))
	ch, cancelSub := m.Subscribe()
	defer cancelSub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	s.active.Store(true)
	waitState(t, ch, true)
	s.active.Store(false)
	waitState(t, ch, false)

	m.Push("presentation", true)
	waitState(t, ch, true)
	m.Push("presentation", false)
	waitState(t, ch, false)

	cancel()
	<-done
	if s.calls.Load() < 2 {
		t.Fatal("not polled repeatedly")
	}
}

func waitState(t *testing.T, ch <-chan State, want bool) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case st := <-ch:
			if st.Restrained == want {
				return
			}
		case <-timeout:
			t.Fatalf("no state change to restrained=%v", want)
		}
	}
}

func TestPolicyAllow(t *testing.T) {
	tests := []struct {
		name       string
		restrained bool
		enabled    bool
		dnd        bool
		kind       Kind
		want       Decision
	}{
		{"free spontaneous", false, true, false, Spontaneous, Deliver},
		{"free user", false, true, false, UserInitiated, Deliver},
		{"restrained spontaneous", true, true, false, Spontaneous, Defer},
		{"restrained user", true, true, false, UserInitiated, Deliver},
		{"restrained but disabled", true, false, false, Spontaneous, Deliver},
		{"dnd spontaneous", false, true, true, Spontaneous, Defer},
		{"dnd user", false, true, true, UserInitiated, Deliver},
		{"dnd while disabled", false, false, true, Spontaneous, Defer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &fakeSource{}
			src.set(tt.restrained, "call")
			c := &clock{time.Unix(1000, 0)}
			p := NewPolicy(src, WithClock(c.now))
			p.SetEnabled(tt.enabled)
			if tt.dnd {
				p.DoNotDisturbUntil(c.t.Add(time.Hour))
			}
			if got := p.Allow(tt.kind); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestPolicyDeferDedupeFlushCap(t *testing.T) {
	src := &fakeSource{}
	src.set(true, "call")
	p := NewPolicy(src, WithMaxQueue(3))
	var flushed [][]Item
	p.SetFlush(func(it []Item) { flushed = append(flushed, it) })

	if p.Submit(UserInitiated, Item{Key: "u"}) != Deliver {
		t.Fatal("user item deferred")
	}
	for _, k := range []string{"a", "b", "a", "c", "d"} {
		if p.Submit(Spontaneous, Item{Key: k, Payload: k}) != Defer {
			t.Fatal("not deferred")
		}
	}
	// a replaced (moved to end): queue b a c d -> cap 3 drops b.
	if p.Pending() != 3 || p.Dropped() != 1 {
		t.Fatalf("pending %d dropped %d", p.Pending(), p.Dropped())
	}
	p.Reevaluate()
	if len(flushed) != 0 {
		t.Fatal("flushed while restrained")
	}
	src.set(false)
	p.Reevaluate()
	if len(flushed) != 1 || len(flushed[0]) != 3 {
		t.Fatalf("flushed %+v", flushed)
	}
	var keys string
	for _, it := range flushed[0] {
		keys += it.Key
	}
	if keys != "acd" {
		t.Fatalf("order %q", keys)
	}
	p.Reevaluate()
	if len(flushed) != 1 {
		t.Fatal("double flush")
	}
	if p.Submit(Spontaneous, Item{Key: "x"}) != Deliver {
		t.Fatal("deferred when free")
	}
}

func TestPolicyDNDExpiry(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	p := NewPolicy(nil, WithClock(c.now))
	n := 0
	p.SetFlush(func(it []Item) { n += len(it) })
	p.DoNotDisturbUntil(c.t.Add(10 * time.Minute))
	if st := p.State(); !st.Restrained || st.Reasons[0] != ReasonDND {
		t.Fatalf("state %+v", st)
	}
	p.Submit(Spontaneous, Item{Key: "a"})
	c.t = c.t.Add(9 * time.Minute)
	p.Reevaluate()
	if n != 0 || p.Allow(Spontaneous) != Defer {
		t.Fatal("expired early")
	}
	c.t = c.t.Add(time.Minute)
	if p.Allow(Spontaneous) != Deliver || !p.DNDUntil().IsZero() {
		t.Fatal("not expired")
	}
	p.Reevaluate()
	if n != 1 {
		t.Fatalf("flushed %d", n)
	}
	p.DoNotDisturbUntil(c.t.Add(time.Hour))
	p.DoNotDisturbUntil(time.Time{})
	if p.Allow(Spontaneous) != Deliver {
		t.Fatal("clear failed")
	}
}

func TestPolicyWatchFlushesOnMonitorChange(t *testing.T) {
	s := &fakeSignal{name: "call"}
	s.active.Store(true)
	m := NewMonitor([]Signal{s}, WithInterval(10*time.Millisecond))
	p := NewPolicy(m)
	flushed := make(chan []Item, 1)
	p.SetFlush(func(it []Item) { flushed <- it })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	go p.Watch(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for p.Submit(Spontaneous, Item{Key: "a"}) != Defer {
		if time.Now().After(deadline) {
			t.Fatal("never restrained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.active.Store(false)
	select {
	case it := <-flushed:
		if len(it) != 1 {
			t.Fatalf("%+v", it)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no flush")
	}
}
