package displays

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

type fakeBackend struct {
	mu      sync.Mutex
	ds      []contracts.Display
	err     error
	changed chan struct{}
	closed  bool
}

func newFake(ds ...contracts.Display) *fakeBackend {
	return &fakeBackend{ds: ds, changed: make(chan struct{}, 64)}
}

func (f *fakeBackend) Displays() ([]contracts.Display, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contracts.Display(nil), f.ds...), f.err
}
func (f *fakeBackend) Changed() <-chan struct{} { return f.changed }
func (f *fakeBackend) Close() error             { f.closed = true; return nil }

// set replaces the displays and signals a change.
func (f *fakeBackend) set(ds ...contracts.Display) {
	f.mu.Lock()
	f.ds = ds
	f.mu.Unlock()
	f.changed <- struct{}{}
}

func disp(id string, x, w, h int, scale float64) contracts.Display {
	return contracts.Display{
		ID: contracts.DisplayID(id), Name: id,
		Bounds: contracts.RectXYWH(float64(x), 0, float64(w)/scale, float64(h)/scale),
		Native: contracts.PixelSize{W: w, H: h}, Scale: scale, Primary: x == 0,
	}
}

const testDebounce = 20 * time.Millisecond

func newTracker(t *testing.T, b Backend) *Tracker {
	t.Helper()
	tr, err := NewTracker(context.Background(), b, testDebounce)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func recv(t *testing.T, ch <-chan contracts.Topology) contracts.Topology {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for topology")
	}
	return contracts.Topology{}
}

func TestTrackerVersioning(t *testing.T) {
	a := disp("DP-1", 0, 1920, 1080, 1)
	b := disp("HDMI-1", 1920, 1920, 1080, 1)
	tests := []struct {
		name    string
		next    []contracts.Display
		bumped  bool
		wantLen int
	}{
		{"added", []contracts.Display{a, b}, true, 2},
		{"removed", nil, true, 0},
		{"scale", []contracts.Display{disp("DP-1", 0, 1920, 1080, 1.5)}, true, 1},
		{"bounds", []contracts.Display{disp("DP-1", 100, 1920, 1080, 1)}, true, 1},
		{"rotation", []contracts.Display{func() contracts.Display { d := a; d.Rotation = 90; return d }()}, true, 1},
		{"primary", []contracts.Display{func() contracts.Display { d := a; d.Primary = false; return d }()}, true, 1},
		{"native", []contracts.Display{disp("DP-1", 0, 2560, 1440, 1)}, true, 1},
		{"identical", []contracts.Display{a}, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb := newFake(a)
			tr := newTracker(t, fb)
			w, err := tr.Watch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			fb.set(tc.next...)
			if tc.bumped {
				got := recv(t, w)
				if got.Version != 2 || len(got.Displays) != tc.wantLen {
					t.Fatalf("got v%d with %d displays", got.Version, len(got.Displays))
				}
				return
			}
			select {
			case got := <-w:
				t.Fatalf("unexpected change v%d", got.Version)
			case <-time.After(5 * testDebounce):
			}
			cur, _ := tr.Topology(context.Background())
			if cur.Version != 1 {
				t.Fatalf("version %d", cur.Version)
			}
		})
	}
}

func TestTrackerDebounceCoalescesBurst(t *testing.T) {
	a := disp("DP-1", 0, 1920, 1080, 1)
	fb := newFake(a)
	tr := newTracker(t, fb)
	w, _ := tr.Watch(context.Background())
	// A replug: remove, add back with another mode, in quick succession.
	fb.set()
	fb.set(disp("DP-1", 0, 1280, 720, 1))
	fb.set(disp("DP-1", 0, 2560, 1440, 1))
	got := recv(t, w)
	if got.Version != 2 || got.Displays[0].Native.W != 2560 {
		t.Fatalf("got %+v", got)
	}
	select {
	case extra := <-w:
		t.Fatalf("second bump v%d", extra.Version)
	case <-time.After(5 * testDebounce):
	}
}

func TestTrackerWatchKeepsLatestAndDoesNotBlock(t *testing.T) {
	fb := newFake(disp("DP-1", 0, 1920, 1080, 1))
	tr := newTracker(t, fb)
	slow, _ := tr.Watch(context.Background())
	fast, _ := tr.Watch(context.Background())
	for i := 1; i <= 3; i++ {
		fb.set(disp("DP-1", 0, 1920+i*10, 1080, 1))
		if got := recv(t, fast); got.Version != uint64(i+1) {
			t.Fatalf("fast got v%d, want %d", got.Version, i+1)
		}
	}
	// The slow subscriber never read; it must see only the newest.
	if got := recv(t, slow); got.Version != 4 {
		t.Fatalf("slow got v%d, want 4", got.Version)
	}
}

func TestTrackerWatchContextEndsChannel(t *testing.T) {
	tr := newTracker(t, newFake(disp("DP-1", 0, 1920, 1080, 1)))
	ctx, cancel := context.WithCancel(context.Background())
	w, _ := tr.Watch(ctx)
	cancel()
	select {
	case _, ok := <-w:
		if ok {
			t.Fatal("unexpected value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed")
	}
}

func TestTrackerBackendFailure(t *testing.T) {
	fb := newFake(disp("DP-1", 0, 1920, 1080, 1))
	tr := newTracker(t, fb)
	w, _ := tr.Watch(context.Background())
	fb.mu.Lock()
	fb.err = errors.New("connection lost")
	fb.mu.Unlock()
	close(fb.changed)
	select {
	case _, ok := <-w:
		if ok {
			t.Fatal("unexpected value")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch not closed")
	}
	if _, err := tr.Topology(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if !tr.IsStale(contracts.CaptureMetadata{TopologyVersion: 1}) {
		t.Fatal("must be stale after backend failure")
	}
}

func TestStaleness(t *testing.T) {
	a := disp("DP-1", 0, 1920, 1080, 1)
	b := disp("HDMI-1", 1920, 1920, 1080, 1)
	fb := newFake(a, b)
	tr := newTracker(t, fb)
	w, _ := tr.Watch(context.Background())

	meta := func(id string, v uint64) contracts.CaptureMetadata {
		return contracts.CaptureMetadata{DisplayID: contracts.DisplayID(id), TopologyVersion: v}
	}
	if err := tr.RequireFresh(meta("DP-1", 1)); err != nil {
		t.Fatalf("fresh capture rejected: %v", err)
	}

	fb.set(a, disp("HDMI-1", 1920, 1920, 1080, 2))
	// Debounce window: the change is known but not yet applied.
	time.Sleep(testDebounce / 4)
	if !tr.IsStale(meta("DP-1", 1)) {
		t.Fatal("pending change must read as stale")
	}
	recv(t, w)
	err := tr.RequireFresh(meta("DP-1", 1))
	if !errors.Is(err, contracts.ErrStale) || err.Error() != "stale screen state: topology changed" {
		t.Fatalf("err = %v", err)
	}
	if tr.IsStale(meta("DP-1", 2)) {
		t.Fatal("capture at the new version is fresh")
	}

	fb.set(a)
	recv(t, w)
	err = tr.RequireFresh(meta("HDMI-1", 3))
	if !errors.Is(err, contracts.ErrStale) || err.Error() != "stale screen state: display removed" {
		t.Fatalf("err = %v", err)
	}
	if tr.IsStale(meta("DP-1", 3)) {
		t.Fatal("remaining display at current version is fresh")
	}
}

func TestInitialFailureClosesBackend(t *testing.T) {
	fb := newFake()
	fb.err = errors.New("boom")
	if _, err := NewTracker(context.Background(), fb, 0); err == nil {
		t.Fatal("expected error")
	}
	if !fb.closed {
		t.Fatal("backend not closed")
	}
}
