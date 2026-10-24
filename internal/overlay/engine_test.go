package overlay

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

type fakeSurface struct {
	display contracts.DisplayID
	frames  []*image.RGBA
	closed  bool
	host    *fakeHost
}

func (s *fakeSurface) Display() contracts.DisplayID { return s.display }

// Present keeps a copy: like the real surfaces, the engine reuses its frame
// buffer after Present returns.
func (s *fakeSurface) Present(_ context.Context, f *image.RGBA) error {
	c := image.NewRGBA(f.Bounds())
	copy(c.Pix, f.Pix)
	s.frames = append(s.frames, c)
	return nil
}
func (s *fakeSurface) SetInteractive(contracts.Rect) error { return nil }
func (s *fakeSurface) Close() error                        { s.closed = true; return nil }

type fakeHost struct{ opened []*fakeSurface }

func (h *fakeHost) Open(_ context.Context, d contracts.DisplayID) (contracts.OverlaySurface, error) {
	s := &fakeSurface{display: d, host: h}
	h.opened = append(h.opened, s)
	return s, nil
}

// open returns surfaces that are currently not closed.
func (h *fakeHost) open() []*fakeSurface {
	var out []*fakeSurface
	for _, s := range h.opened {
		if !s.closed {
			out = append(out, s)
		}
	}
	return out
}

var (
	dispA = contracts.Display{ID: "a", Bounds: contracts.RectXYWH(0, 0, 200, 100), Native: contracts.PixelSize{W: 400, H: 200}, Scale: 2}
	dispB = contracts.Display{ID: "b", Bounds: contracts.RectXYWH(-100, 0, 100, 100), Native: contracts.PixelSize{W: 100, H: 100}, Scale: 1}
)

type timers struct{ pending []func() }

func (t *timers) after(_ time.Duration, f func()) func() bool {
	i := len(t.pending)
	t.pending = append(t.pending, f)
	return func() bool { t.pending[i] = nil; return true }
}

func (t *timers) fireAll() {
	for _, f := range t.pending {
		if f != nil {
			f()
		}
	}
}

func newTestEngine(topo *contracts.Topology) (*Engine, *fakeHost, *timers) {
	host := &fakeHost{}
	e := NewEngine(host, func(context.Context) (contracts.Topology, error) { return *topo, nil })
	// Frames are checked in their settled look and must not change under
	// the test from a background animation loop.
	e.motion = false
	tm := &timers{}
	e.afterFunc = tm.after
	for _, d := range topo.Displays {
		e.RegisterCapture(contracts.CaptureMetadata{
			ID: contracts.CaptureID("cap-" + string(d.ID)), DisplayID: d.ID,
			Native: d.Native, Bounds: d.Bounds, Scale: d.Scale, TopologyVersion: topo.Version,
		})
	}
	return e, host, tm
}

var s1 = contracts.SessionRef{ID: "s1", Generation: 1}

func ring(id string, ref contracts.SessionRef, capture contracts.CaptureID) Command {
	return Command{
		Op: OpShow, ID: id, Session: ref, Capture: capture, Kind: KindRing,
		Points: []contracts.Point{{X: 0.5, Y: 0.5}}, Radius: 0.1, Segment: -1,
	}
}

func lastFrame(t *testing.T, s *fakeSurface) *image.RGBA {
	t.Helper()
	if len(s.frames) == 0 {
		t.Fatal("no frame presented")
	}
	return s.frames[len(s.frames)-1]
}

func TestShowDrawsOnTheRightDisplayAtNativeSize(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA, dispB}}
	e, host, _ := newTestEngine(&topo)
	if err := e.Apply(context.Background(), ring("r", s1, "cap-a")); err != nil {
		t.Fatal(err)
	}
	open := host.open()
	if len(open) != 1 || open[0].display != "a" {
		t.Fatalf("open surfaces = %+v, want one on a", open)
	}
	f := lastFrame(t, open[0])
	if f.Bounds().Dx() != 400 || f.Bounds().Dy() != 200 {
		t.Fatalf("frame %v, want native 400x200", f.Bounds())
	}
	// Ring of radius 0.1*200 logical = 40 native px around the center
	// (200,100): the stroke is on the circle, the center stays clear.
	if f.RGBAAt(200, 100).A != 0 {
		t.Error("ring center is painted")
	}
	if f.RGBAAt(240, 100).A == 0 {
		t.Error("ring stroke not painted")
	}
}

func TestEmptySceneClosesSurface(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, _ := newTestEngine(&topo)
	ctx := context.Background()
	_ = e.Apply(ctx, ring("r", s1, "cap-a"))
	if err := e.Apply(ctx, Command{Op: OpRemove, ID: "r", Session: s1}); err != nil {
		t.Fatal(err)
	}
	if len(host.open()) != 0 {
		t.Fatal("surface left open after last annotation removed")
	}
}

func TestClearAndNewSession(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA, dispB}}
	e, host, _ := newTestEngine(&topo)
	ctx := context.Background()
	_ = e.Apply(ctx, ring("r1", s1, "cap-a"))
	_ = e.Apply(ctx, ring("r2", s1, "cap-b"))
	if len(host.open()) != 2 {
		t.Fatalf("want 2 open surfaces")
	}

	// A newer generation replaces the whole scene.
	s2 := contracts.SessionRef{ID: "s1", Generation: 2}
	_ = e.Apply(ctx, ring("r3", s2, "cap-b"))
	open := host.open()
	if len(open) != 1 || open[0].display != "b" {
		t.Fatalf("after new generation open = %d surfaces", len(open))
	}

	// Late command of the old generation is dropped.
	_ = e.Apply(ctx, ring("late", s1, "cap-a"))
	if len(host.open()) != 1 {
		t.Fatal("stale generation command was drawn")
	}

	e.Clear(ctx)
	if len(host.open()) != 0 {
		t.Fatal("Clear left surfaces open")
	}
}

func TestLifetimeExpiry(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, tm := newTestEngine(&topo)
	cmd := ring("r", s1, "cap-a")
	cmd.Lifetime = time.Second
	_ = e.Apply(context.Background(), cmd)
	tm.fireAll()
	if len(host.open()) != 0 {
		t.Fatal("expired annotation still shown")
	}
}

func TestSegmentSwitch(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, _ := newTestEngine(&topo)
	ctx := context.Background()
	a := ring("seg0", s1, "cap-a")
	a.Segment = 0
	b := ring("persist", s1, "cap-a")
	b.Points = []contracts.Point{{X: 0.2, Y: 0.2}}
	_ = e.Apply(ctx, a)
	_ = e.Apply(ctx, b)
	_ = e.SetSegment(ctx, s1, 1)
	f := lastFrame(t, host.open()[0])
	if f.RGBAAt(240, 100).A != 0 {
		t.Error("segment 0 annotation still drawn")
	}
	if f.RGBAAt(80+40, 40).A == 0 {
		t.Error("segment-less annotation was removed")
	}
}

func TestStaleTopologyRejected(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, _ := newTestEngine(&topo)
	topo.Version = 2
	err := e.Apply(context.Background(), ring("r", s1, "cap-a"))
	if !errors.Is(err, contracts.ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if len(host.open()) != 0 {
		t.Fatal("stale command opened a surface")
	}
}

func TestUnknownCapture(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, _, _ := newTestEngine(&topo)
	if err := e.Apply(context.Background(), ring("r", s1, "nope")); !errors.Is(err, ErrUnknownCapture) {
		t.Fatalf("err = %v", err)
	}
}

// With motion on, the engine keeps presenting frames while a hint is shown
// and stops the loop once the scene is empty.
func TestAnimationLoopRunsOnlyWhileShown(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, _ := newTestEngine(&topo)
	e.mu.Lock()
	e.motion = true
	e.mu.Unlock()
	ctx := context.Background()
	if err := e.Apply(ctx, ring("r", s1, "cap-a")); err != nil {
		t.Fatal(err)
	}
	surf := host.open()[0]
	frames := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(surf.frames)
	}
	deadline := time.Now().Add(2 * time.Second)
	for frames() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := frames(); n < 4 {
		t.Fatalf("only %d frames while animating", n)
	}
	e.Clear(ctx)
	for {
		e.mu.Lock()
		running := e.animating
		e.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("animation loop still running after Clear")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !surf.closed {
		t.Error("surface left open after Clear")
	}
}

func TestEndSession(t *testing.T) {
	s2 := contracts.SessionRef{ID: "s1", Generation: 2}
	other := contracts.SessionRef{ID: "s9", Generation: 1}
	tests := []struct {
		name     string
		shown    contracts.SessionRef // generation whose hint is on screen
		ended    contracts.SessionRef
		wantOpen int
	}{
		{"current generation is cleared", s1, s1, 0},
		{"ending a newer generation clears an older scene", s1, s2, 0},
		{"older generation leaves the newer scene", s2, s1, 1},
		{"other session leaves the scene", s1, other, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
			e, host, _ := newTestEngine(&topo)
			ctx := context.Background()
			if err := e.Apply(ctx, ring("r", tt.shown, "cap-a")); err != nil {
				t.Fatal(err)
			}
			e.EndSession(ctx, tt.ended)
			if n := e.Shown(); n != tt.wantOpen {
				t.Fatalf("%d hints shown, want %d", n, tt.wantOpen)
			}
			if n := len(host.open()); n != tt.wantOpen {
				t.Fatalf("%d surfaces open, want %d", n, tt.wantOpen)
			}
			// A late command of the ended generation must not draw again,
			// even one of another session ID that would replace the scene.
			if err := e.Apply(ctx, ring("late", tt.ended, "cap-a")); err != nil {
				t.Fatal(err)
			}
			if n := len(host.open()); n != tt.wantOpen {
				t.Fatalf("late command of an ended generation changed the scene: %d open", n)
			}
		})
	}
}

func TestApplyWithDoneContextDrawsNothing(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{dispA}}
	e, host, _ := newTestEngine(&topo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Apply(ctx, ring("r", s1, "cap-a")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply = %v, want context.Canceled", err)
	}
	if len(host.open()) != 0 {
		t.Fatal("a cancelled generation drew a hint")
	}
}
