package walkthrough

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// frame is a w×h gray image with a w/4 square of brightness box at the
// top left (none for box < 0).
func frame(w, h int, bg uint8, box int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := bg
			if box >= 0 && x < w/4 && y < w/4 {
				v = uint8(box)
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return img
}

func thumbOf(img *image.RGBA) *Thumb {
	var t Thumb
	t.From(img, thumbSide)
	return &t
}

func TestThumb(t *testing.T) {
	tests := []struct {
		name string
		img  *image.RGBA
		w, h int
		at   map[[2]int]uint8
	}{
		{"wide display", frame(1600, 900, 100, -1), 80, 45, map[[2]int]uint8{{0, 0}: 100, {79, 44}: 100}},
		{"tall area", frame(90, 300, 10, -1), 24, 80, map[[2]int]uint8{{0, 0}: 10}},
		{"small stays", frame(40, 20, 50, -1), 40, 20, map[[2]int]uint8{{39, 19}: 50}},
		{"box shows", frame(800, 400, 0, 240), 80, 40, map[[2]int]uint8{{0, 0}: 240, {19, 19}: 240, {20, 20}: 0}},
		{"color to luma", func() *image.RGBA {
			img := image.NewRGBA(image.Rect(0, 0, 10, 10))
			for i := 0; i < len(img.Pix); i += 4 {
				img.Pix[i], img.Pix[i+3] = 255, 255 // pure red
			}
			return img
		}(), 10, 10, map[[2]int]uint8{{5, 5}: 76}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			th := thumbOf(tt.img)
			if th.W != tt.w || th.H != tt.h || len(th.Pix) != tt.w*tt.h {
				t.Fatalf("thumb %dx%d (%d px), want %dx%d", th.W, th.H, len(th.Pix), tt.w, tt.h)
			}
			for xy, want := range tt.at {
				if got := th.Pix[xy[1]*th.W+xy[0]]; got != want {
					t.Errorf("cell %v = %d, want %d", xy, got, want)
				}
			}
			th.Zero()
			for _, p := range th.Pix {
				if p != 0 {
					t.Fatal("Zero left pixels")
				}
			}
		})
	}
}

func TestChanged(t *testing.T) {
	base := thumbOf(frame(800, 400, 100, -1))
	tests := []struct {
		name     string
		other    *Thumb
		min, max float64
	}{
		{"same", thumbOf(frame(800, 400, 100, -1)), 0, 0},
		{"noise under delta", thumbOf(frame(800, 400, 100+pixelDelta, -1)), 0, 0},
		{"everything", thumbOf(frame(800, 400, 200, -1)), 1, 1},
		{"a box an eighth of the screen", thumbOf(frame(800, 400, 100, 250)), 0.12, 0.13},
		{"other size", thumbOf(frame(400, 400, 100, -1)), 1, 1},
	}
	for _, tt := range tests {
		if got := changed(tt.other, base); got < tt.min || got > tt.max {
			t.Errorf("%s: changed %.3f, want %.2f..%.2f", tt.name, got, tt.min, tt.max)
		}
	}
}

func TestDetector(t *testing.T) {
	const (
		A = iota // baseline
		B        // the step done: a box appeared
		C        // something else
		D        // a different screen size
	)
	thumbs := []*Thumb{
		thumbOf(frame(800, 400, 100, -1)),
		thumbOf(frame(800, 400, 100, 250)),
		thumbOf(frame(800, 400, 30, -1)),
		thumbOf(frame(400, 400, 100, -1)),
	}
	type sample struct {
		frame int
		at    time.Duration // since the first sample
		want  verdict
	}
	s := func(f int, sec float64, v verdict) sample {
		return sample{f, time.Duration(sec * float64(time.Second)), v}
	}
	tests := []struct {
		name    string
		samples []sample
	}{
		{"unchanged screen never asks", []sample{s(A, 0, keepWatching), s(A, 1.5, keepWatching), s(A, 3, keepWatching), s(A, 4.5, keepWatching)}},
		{"settled change asks once", []sample{s(A, 0, keepWatching), s(B, 1.5, keepWatching), s(B, 3, askModel), s(B, 4.5, keepWatching), s(B, 6, keepWatching)}},
		{"flicker is ignored", []sample{s(A, 0, keepWatching), s(B, 1.5, keepWatching), s(A, 3, keepWatching), s(B, 4.5, keepWatching), s(A, 6, keepWatching)}},
		{"animation never settles", []sample{s(A, 0, keepWatching), s(B, 1.5, keepWatching), s(C, 3, keepWatching), s(B, 4.5, keepWatching), s(C, 6, keepWatching)}},
		{"another size is a new baseline", []sample{s(A, 0, keepWatching), s(D, 1.5, keepWatching), s(D, 3, keepWatching), s(D, 4.5, keepWatching)}},
		{"checks are spaced", []sample{
			s(A, 0, keepWatching), s(B, 1, keepWatching), s(B, 2, askModel),
			// A further settled change 2 s later waits for the gap.
			s(C, 3, keepWatching), s(C, 4, keepWatching), s(C, 5, keepWatching), s(C, 7, askModel),
		}},
		{"checks are capped", []sample{
			s(A, 0, keepWatching), s(B, 1, keepWatching), s(B, 2, askModel),
			s(C, 10, keepWatching), s(C, 11, askModel),
			s(B, 20, keepWatching), s(B, 21, askModel),
			s(C, 30, keepWatching), s(C, 31, askModel),
			s(B, 40, keepWatching), s(B, 41, giveUp),
		}},
	}
	t0 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d detector
			defer d.close()
			for i, sm := range tt.samples {
				// observe must not keep or alter the caller's thumbnail.
				in := *thumbs[sm.frame]
				in.Pix = append([]uint8(nil), in.Pix...)
				if got := d.observe(&in, t0.Add(sm.at)); got != sm.want {
					t.Fatalf("sample %d (frame %d at %v): verdict %d, want %d", i, sm.frame, sm.at, got, sm.want)
				}
			}
		})
	}
}

func TestWatchArea(t *testing.T) {
	tests := []struct {
		name string
		in   contracts.Rect
		want contracts.Rect
	}{
		{"no hints: whole display", contracts.Rect{}, contracts.Rect{}},
		{"point", contracts.RectXYWH(500, 300, 0, 0), contracts.RectXYWH(500-areaMargin, 300-areaMargin, 2*areaMargin, 2*areaMargin)},
		{"large area grows by half", contracts.RectXYWH(100, 100, 400, 200), contracts.RectXYWH(-100, -100, 800, 600)},
	}
	for _, tt := range tests {
		if got := watchArea(tt.in); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestFromControlCall(t *testing.T) {
	tests := []struct {
		args string
		want Control
		ok   bool
	}{
		{`{"action":"next"}`, Control{Action: CmdNext}, true},
		{`{"action":"next","reason":"the panel is open"}`, Control{Action: CmdNext, Reason: "the panel is open"}, true},
		{`{"action":"previous"}`, Control{Action: CmdPrev}, true},
		{`{"action":"stop"}`, Control{Action: CmdStop}, true},
		{`{"action":"goto","step":3}`, Control{Action: CmdGoto, Step: 3}, true},
		{`{"action":"goto","step":4}`, Control{}, false},
		{`{"action":"goto"}`, Control{}, false},
		{`{"action":"next","step":2}`, Control{}, false},
		{`{"action":"jump"}`, Control{}, false},
		{`{}`, Control{}, false},
		{`{"action":"next","why":"x"}`, Control{}, false},
		{`{"action":"next","reason":"` + strings.Repeat("x", maxReasonLen+1) + `"}`, Control{}, false},
		{`[`, Control{}, false},
	}
	for _, tt := range tests {
		got, err := FromControlCall(json.RawMessage(tt.args), 3)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("%s: %+v, %v; want %+v ok %v", tt.args, got, err, tt.want, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidPlan) {
			t.Errorf("%s: err %v is not ErrInvalidPlan", tt.args, err)
		}
	}
}

func TestGoToAndDone(t *testing.T) {
	st := State{Plan: plan(4), Status: StatusRunning}
	var err error
	st, _, _ = st.Apply(CmdNext)  // 0 done → 1
	st, _, _ = st.GoTo(3)         // jump over 1, 2
	st, _, _ = st.Apply(CmdPrev)  // 2
	st, _, _ = st.Apply(CmdPause) // paused at 2
	st, eff, err := st.GoTo(1)
	if err != nil || eff != EffectPresent || st.Index != 1 || st.Status != StatusRunning {
		t.Fatalf("goto while paused: %+v eff %d err %v", st, eff, err)
	}
	for i, want := range []bool{true, false, false, false} {
		if st.IsDone(i) != want {
			t.Errorf("step %d done %v, want %v", i, st.IsDone(i), want)
		}
	}
	if _, _, err := st.GoTo(4); !errors.Is(err, ErrUnknown) {
		t.Errorf("goto past the end: %v", err)
	}
	if _, _, err := (State{Status: StatusDone, Plan: plan(2)}).GoTo(0); !errors.Is(err, ErrNotActive) {
		t.Errorf("goto when done: %v", err)
	}
	b := st.view().Brief()
	for _, want := range []string{`Goal: "goal"`, `1. "t0" [done];`, `2. "t1" [current];`, `3. "t2";`, `step 2 of 4: "t1", instruction: "s1"`} {
		if !strings.Contains(b, want) {
			t.Errorf("brief %q lacks %q", b, want)
		}
	}
}

// fakeScreen is a capture source whose top-left quarter the test can
// light up, as if the user's click opened something there.
type fakeScreen struct {
	mu   sync.Mutex
	box  int
	reqs []contracts.CaptureRequest
}

func (f *fakeScreen) set(box int) { f.mu.Lock(); f.box = box; f.mu.Unlock() }

func (f *fakeScreen) Capture(_ context.Context, req contracts.CaptureRequest) ([]*contracts.Capture, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return []*contracts.Capture{{Image: frame(160, 90, 100, f.box)}}, nil
}

func (f *fakeScreen) captures() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

// clock moves on by step every reading, so checks are never too close.
type clock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

func TestServiceAutoAdvance(t *testing.T) {
	area := contracts.RectXYWH(1000, 500, 40, 20)
	next := &Control{Action: CmdNext}
	tests := []struct {
		name    string
		area    contracts.Rect
		verdict Verdict
		// moves: the walkthrough is at step 1 after the change.
		moves  bool
		region bool
	}{
		{"done: next step", area, Verdict{Control: next}, true, true},
		{"not done yet", area, Verdict{}, false, true},
		{"revised", contracts.Rect{}, Verdict{Step: Step{Title: "fixed", Say: "fixed say"}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			screen := &fakeScreen{box: -1}
			fp := &fakePresenter{area: tt.area, check: func(Presentation) Verdict { return tt.verdict }}
			ck := &clock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), step: 3 * time.Second}
			s := New(Config{Presenter: fp, Now: ck.now, Watch: &Watch{Capturer: screen, Interval: 5 * time.Millisecond}})
			defer s.Close()
			if err := s.Start(ctx, plan(3), ""); err != nil {
				t.Fatal(err)
			}
			wait := func(what string, cond func() bool) {
				t.Helper()
				for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("%s: timed out; view %+v", what, s.View())
					}
				}
			}
			wait("watching", func() bool { return s.View().Watching })
			// A quiet screen asks nothing.
			wait("samples", func() bool { return screen.captures() >= 5 })
			fp.mu.Lock()
			early := len(fp.checked)
			fp.mu.Unlock()
			if early != 0 {
				t.Fatalf("model asked %d times about an unchanged screen", early)
			}
			screen.set(250)
			wait("checked", func() bool { fp.mu.Lock(); defer fp.mu.Unlock(); return len(fp.checked) > 0 })
			fp.mu.Lock()
			p := fp.checked[0]
			fp.mu.Unlock()
			if p.Index != 0 || !strings.Contains(p.Brief, "[current]") || p.Ref.Generation <= 1 {
				t.Errorf("check presentation %+v", p)
			}
			if tt.moves {
				wait("advanced", func() bool { v := s.View(); return v.Index == 1 && v.Done[0] })
			} else {
				time.Sleep(30 * time.Millisecond)
				if v := s.View(); v.Index != 0 {
					t.Fatalf("moved to %d without the model's say-so", v.Index)
				}
			}
			if tt.verdict.Step.Title != "" {
				wait("revised", func() bool { return s.View().Title == "fixed" })
			}
			screen.mu.Lock()
			req := screen.reqs[0]
			screen.mu.Unlock()
			if got := req.Target == contracts.CaptureRegion; got != tt.region {
				t.Errorf("sampled %+v, want region %v", req, tt.region)
			}
			// Paused, the screen is left alone.
			if err := s.Do(ctx, CmdPause); err != nil {
				t.Fatal(err)
			}
			if s.View().Watching {
				t.Error("still watching while paused")
			}
			time.Sleep(10 * time.Millisecond)
			n := screen.captures()
			time.Sleep(30 * time.Millisecond)
			if m := screen.captures(); m != n {
				t.Errorf("%d captures while paused", m-n)
			}
		})
	}
}

func TestServiceControl(t *testing.T) {
	ctx := context.Background()
	fp := &fakePresenter{}
	s := New(Config{Presenter: fp})
	defer s.Close()
	if err := s.Control(ctx, Control{Action: CmdNext}, 0); !errors.Is(err, ErrNotActive) {
		t.Fatalf("before start: %v", err)
	}
	if err := s.Start(ctx, plan(4), ""); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		c     Control
		at    int
		index int
		err   error
	}{
		{Control{Action: CmdNext}, 0, 1, nil},
		{Control{Action: CmdNext}, 0, 1, ErrMoved}, // decided on step 1, now at 2
		{Control{Action: CmdGoto, Step: 4}, 1, 3, nil},
		{Control{Action: CmdPrev}, 3, 2, nil},
		{Control{Action: CmdGoto, Step: 9}, 2, 2, ErrUnknown},
	}
	for i, tt := range tests {
		if err := s.Control(ctx, tt.c, tt.at); !errors.Is(err, tt.err) {
			t.Fatalf("%d %+v: err %v, want %v", i, tt.c, err, tt.err)
		}
		if v := s.View(); v.Index != tt.index {
			t.Fatalf("%d %+v: at %d, want %d", i, tt.c, v.Index, tt.index)
		}
	}
	if err := s.Revise(ctx, Revision{Title: "new", Say: "new say"}); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); v.Title != "new" || v.Text != "new say" {
		t.Errorf("after Revise: %+v", v)
	}
	if err := s.Revise(ctx, Revision{Title: "", Say: "x"}); err == nil {
		t.Error("empty title accepted")
	}
}
