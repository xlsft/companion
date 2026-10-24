package walkthrough

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/pkg/contracts"
)

func plan(n int) Plan {
	p := Plan{Goal: "goal"}
	for i := range n {
		p.Steps = append(p.Steps, Step{Title: "t" + string(rune('0'+i)), Say: "s" + string(rune('0'+i))})
	}
	return p
}

func TestApply(t *testing.T) {
	run := func(i int) State { return State{Plan: plan(3), Status: StatusRunning, Index: i, Fresh: true} }
	paused := func(i int) State { s := run(i); s.Status = StatusPaused; return s }
	tests := []struct {
		name   string
		from   State
		cmd    Command
		status Status
		index  int
		eff    Effect
		err    error
	}{
		{"next", run(0), CmdNext, StatusRunning, 1, EffectPresent, nil},
		{"next on last finishes", run(2), CmdNext, StatusDone, 2, EffectEnd, nil},
		{"previous", run(2), CmdPrev, StatusRunning, 1, EffectPresent, nil},
		{"previous on first repeats it", run(0), CmdPrev, StatusRunning, 0, EffectPresent, nil},
		{"repeat", run(1), CmdRepeat, StatusRunning, 1, EffectPresent, nil},
		{"pause", run(1), CmdPause, StatusPaused, 1, EffectHush, nil},
		{"pause twice", paused(1), CmdPause, StatusPaused, 1, EffectNone, nil},
		{"resume", paused(1), CmdResume, StatusRunning, 1, EffectPresent, nil},
		{"resume while running", run(1), CmdResume, StatusRunning, 1, EffectNone, nil},
		{"next while paused resumes", paused(0), CmdNext, StatusRunning, 1, EffectPresent, nil},
		{"previous while paused resumes", paused(2), CmdPrev, StatusRunning, 1, EffectPresent, nil},
		{"stop", run(1), CmdStop, StatusStopped, 1, EffectEnd, nil},
		{"stop while paused", paused(1), CmdStop, StatusStopped, 1, EffectEnd, nil},
		{"idle takes nothing", State{Status: StatusIdle}, CmdNext, StatusIdle, 0, EffectNone, ErrNotActive},
		{"done takes nothing", State{Plan: plan(3), Status: StatusDone}, CmdRepeat, StatusDone, 0, EffectNone, ErrNotActive},
		{"stopped takes nothing", State{Plan: plan(3), Status: StatusStopped}, CmdResume, StatusStopped, 0, EffectNone, ErrNotActive},
		{"unknown", run(0), Command("jump"), StatusRunning, 0, EffectNone, ErrUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, eff, err := tt.from.Apply(tt.cmd)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err %v, want %v", err, tt.err)
			}
			if got.Status != tt.status || got.Index != tt.index || eff != tt.eff {
				t.Errorf("got %s/%d effect %d, want %s/%d effect %d", got.Status, got.Index, eff, tt.status, tt.index, tt.eff)
			}
			if eff == EffectPresent && got.Fresh {
				t.Error("a moved-to step must be shown on a new screenshot")
			}
		})
	}
}

func TestFromToolCall(t *testing.T) {
	ring := `[{"kind":"ring","points":[[0.5,0.5]]}]`
	tests := []struct {
		name   string
		args   string
		screen bool
		ok     bool
		shapes []bool // per step: has shapes
	}{
		{"plain", `{"goal":"g","steps":[{"title":"a","say":"b"},{"title":"c","say":"d"}]}`, false, true, []bool{false, false}},
		{"with shapes", `{"goal":"g","steps":[{"title":"a","say":"b","shapes":` + ring + `},{"title":"c","say":"d","shapes":[]}]}`, true, true, []bool{true, false}},
		{"shapes without screen", `{"goal":"g","steps":[{"title":"a","say":"b","shapes":` + ring + `}]}`, false, false, nil},
		{"bad shape", `{"goal":"g","steps":[{"title":"a","say":"b","shapes":[{"kind":"ring","points":[[2,0.5]]}]}]}`, true, false, nil},
		{"unknown shape kind", `{"goal":"g","steps":[{"title":"a","say":"b","shapes":[{"kind":"image_card","points":[[0.5,0.5]]}]}]}`, true, false, nil},
		{"no steps", `{"goal":"g","steps":[]}`, true, false, nil},
		{"no goal", `{"steps":[{"title":"a","say":"b"}]}`, true, false, nil},
		{"empty say", `{"goal":"g","steps":[{"title":"a","say":""}]}`, true, false, nil},
		{"unknown field", `{"goal":"g","steps":[{"title":"a","say":"b","url":"x"}]}`, true, false, nil},
		{"too many steps", `{"goal":"g","steps":[` + strings.Repeat(`{"title":"a","say":"b"},`, MaxSteps) + `{"title":"a","say":"b"}]}`, true, false, nil},
		{"not json", `{`, true, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := FromToolCall(json.RawMessage(tt.args), tt.screen)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok %v", err, tt.ok)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidPlan) {
					t.Errorf("err %v is not ErrInvalidPlan", err)
				}
				return
			}
			for i, want := range tt.shapes {
				if got := len(p.Steps[i].Shapes) > 0; got != want {
					t.Errorf("step %d shapes %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestFromReviseCall(t *testing.T) {
	if r, err := FromReviseCall(json.RawMessage(`{"title":"a","say":"b"}`)); err != nil || r.Title != "a" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{`{"title":"a"}`, `{"title":"","say":"b"}`, `{"title":"a","say":"b","x":1}`} {
		if _, err := FromReviseCall(json.RawMessage(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// memKV is a store.KV stand-in.
type memKV struct {
	mu sync.Mutex
	m  map[string]json.RawMessage
}

func (k *memKV) Get(_ context.Context, key string) (json.RawMessage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return v, nil
}

func (k *memKV) Set(_ context.Context, key string, v json.RawMessage) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]json.RawMessage{}
	}
	k.m[key] = v
	return nil
}

func (k *memKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	p := plan(3)
	p.Steps[0].Shapes = json.RawMessage(`[{"kind":"ring","points":[[0.5,0.5]]}]`)
	tests := []struct {
		name  string
		save  *State
		found bool
	}{
		{"nothing stored", nil, false},
		{"running", &State{ID: "a", Plan: p, Status: StatusRunning, Index: 1, Started: at, Updated: at}, true},
		{"paused", &State{ID: "b", Plan: p, Status: StatusPaused, Index: 2, Started: at, Updated: at}, true},
		{"finished is not kept", &State{ID: "c", Plan: p, Status: StatusDone, Index: 2}, false},
		{"index out of range is dropped", &State{ID: "d", Plan: p, Status: StatusPaused, Index: 3}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewKVStore(store.NewKV(db))
			if err := s.Delete(ctx); err != nil {
				t.Fatal(err)
			}
			if tt.save != nil {
				if err := s.Save(ctx, *tt.save); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := s.Load(ctx)
			if err != nil || ok != tt.found {
				t.Fatalf("Load: ok %v err %v, want ok %v", ok, err, tt.found)
			}
			if !ok {
				return
			}
			w := *tt.save
			if got.ID != w.ID || got.Status != w.Status || got.Index != w.Index || got.Plan.Goal != w.Plan.Goal ||
				len(got.Plan.Steps) != 3 || string(got.Plan.Steps[0].Shapes) != string(w.Plan.Steps[0].Shapes) ||
				!got.Started.Equal(at) {
				t.Errorf("round trip: got %+v, want %+v", got, w)
			}
		})
	}
}

// fakePresenter records showings and speech.
type fakePresenter struct {
	mu     sync.Mutex
	shown  []Presentation
	said   []string
	ended  []contracts.SessionRef
	revise func(Presentation) Step
	// area is where Show says it drew; check answers Check.
	area    contracts.Rect
	check   func(Presentation) Verdict
	checked []Presentation
	block   chan struct{} // Say waits on it when set
	saidCh  chan string
}

func (f *fakePresenter) Show(_ context.Context, p Presentation) (Shown, error) {
	f.mu.Lock()
	f.shown = append(f.shown, p)
	rev := f.revise
	f.mu.Unlock()
	if rev != nil {
		return Shown{Step: rev(p)}, nil
	}
	return Shown{Step: p.Step, Area: f.area}, nil
}

func (f *fakePresenter) Check(ctx context.Context, p Presentation) (Verdict, error) {
	f.mu.Lock()
	f.checked = append(f.checked, p)
	check := f.check
	f.mu.Unlock()
	if check == nil {
		return Verdict{}, nil
	}
	return check(p), nil
}

func (f *fakePresenter) Say(ctx context.Context, text string) error {
	f.mu.Lock()
	f.said = append(f.said, text)
	block := f.block
	f.mu.Unlock()
	if f.saidCh != nil {
		f.saidCh <- text
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakePresenter) End(_ context.Context, ref contracts.SessionRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, ref)
}

func TestServiceRun(t *testing.T) {
	ctx := context.Background()
	fp := &fakePresenter{saidCh: make(chan string, 16)}
	kv := &memKV{}
	s := New(Config{Presenter: fp, Store: NewKVStore(kv), NewID: func() string { return "w1" }})
	defer s.Close()
	said := func(want string) {
		t.Helper()
		select {
		case got := <-fp.saidCh:
			if got != want {
				t.Fatalf("said %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%q not said", want)
		}
	}
	if err := s.Do(ctx, CmdNext); !errors.Is(err, ErrNotActive) {
		t.Fatalf("Do before Start: %v", err)
	}
	if err := s.Start(ctx, plan(3), "cap1"); err != nil {
		t.Fatal(err)
	}
	said("s0")
	steps := []struct {
		cmd  Command
		say  string
		idx  int
		stat Status
	}{
		{CmdNext, "s1", 1, StatusRunning},
		{CmdRepeat, "s1", 1, StatusRunning},
		{CmdPrev, "s0", 0, StatusRunning},
		{CmdNext, "s1", 1, StatusRunning},
		{CmdPause, "", 1, StatusPaused},
		{CmdResume, "s1", 1, StatusRunning},
	}
	for _, st := range steps {
		if err := s.Do(ctx, st.cmd); err != nil {
			t.Fatalf("%s: %v", st.cmd, err)
		}
		if st.say != "" {
			said(st.say)
		}
		if v := s.View(); v.Index != st.idx || v.Status != st.stat {
			t.Fatalf("after %s: view %+v", st.cmd, v)
		}
		stored, ok, _ := NewKVStore(kv).Load(ctx)
		if !ok || stored.Index != st.idx || stored.Status != st.stat {
			t.Fatalf("after %s: stored %+v ok %v", st.cmd, stored, ok)
		}
	}
	fp.mu.Lock()
	first, rest := fp.shown[0], fp.shown[1:]
	fp.mu.Unlock()
	if first.Capture != "cap1" {
		t.Errorf("first step shown on %q, want the plan's screenshot", first.Capture)
	}
	for i, p := range rest {
		if p.Capture != "" {
			t.Errorf("showing %d reused screenshot %q", i+1, p.Capture)
		}
		if p.Ref.Generation <= first.Ref.Generation {
			t.Errorf("showing %d generation %d not after %d", i+1, p.Ref.Generation, first.Ref.Generation)
		}
	}
	s.Stop(ctx)
	if s.Active() {
		t.Error("active after stop")
	}
	if _, ok, _ := NewKVStore(kv).Load(ctx); ok {
		t.Error("stopped walkthrough still stored")
	}
	fp.mu.Lock()
	if len(fp.ended) != 1 {
		t.Errorf("hints ended %d times, want 1", len(fp.ended))
	}
	fp.mu.Unlock()
}

func TestServiceRestoreAndRevise(t *testing.T) {
	ctx := context.Background()
	kv := &memKV{}
	if err := NewKVStore(kv).Save(ctx, State{ID: "old", Plan: plan(3), Status: StatusRunning, Index: 2}); err != nil {
		t.Fatal(err)
	}
	fp := &fakePresenter{saidCh: make(chan string, 4), revise: func(p Presentation) Step {
		return Step{Title: "revised", Say: "revised say"}
	}}
	s := New(Config{Presenter: fp, Store: NewKVStore(kv)})
	defer s.Close()
	if err := s.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	v := s.View()
	if v.Status != StatusPaused || v.Index != 2 || v.Prev != "t1" {
		t.Fatalf("restored view %+v, want paused at 2", v)
	}
	if err := s.Do(ctx, CmdResume); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-fp.saidCh:
		if got != "revised say" {
			t.Fatalf("said %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing said")
	}
	if v := s.View(); v.Title != "revised" {
		t.Errorf("view title %q after revision", v.Title)
	}
	stored, _, _ := NewKVStore(kv).Load(ctx)
	if stored.Plan.Steps[2].Title != "revised" {
		t.Errorf("revision not stored: %+v", stored.Plan.Steps[2])
	}
	if err := s.Do(ctx, CmdNext); err != nil {
		t.Fatal(err)
	}
	if v := s.View(); v.Status != StatusDone || v.Active() {
		t.Errorf("after the last step: %+v", v)
	}
}

func TestServiceHushStopsSpeech(t *testing.T) {
	ctx := context.Background()
	fp := &fakePresenter{saidCh: make(chan string, 4), block: make(chan struct{})}
	s := New(Config{Presenter: fp})
	defer s.Close()
	if err := s.Start(ctx, plan(2), ""); err != nil {
		t.Fatal(err)
	}
	<-fp.saidCh
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	s.Hush()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("speech not cancelled by Hush")
	}
	if !s.Active() {
		t.Error("Hush ended the walkthrough")
	}
}
