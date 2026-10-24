package dictation

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

type fakeRecorder struct {
	mu        sync.Mutex
	startErr  error
	text      string
	finishErr error
	started   int
	cancelled int
	finished  int
}

func (f *fakeRecorder) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return f.startErr
}

func (f *fakeRecorder) Finish(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished++
	return f.text, f.finishErr
}

func (f *fakeRecorder) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled++
}

type fakeInserter struct {
	mu    sync.Mutex
	err   error
	block bool // wait for ctx
	got   []string
}

func (f *fakeInserter) InsertText(ctx context.Context, text string) error {
	f.mu.Lock()
	f.got = append(f.got, text)
	block, err := f.block, f.err
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeInserter) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// clock is a settable time source for the hold threshold.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func waitPhase(t *testing.T, s *Service, p Phase) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := s.State(); st.Phase == p {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase %v not reached, at %v", p, s.State())
	return State{}
}

// waitIdle waits until the dictation in flight has ended.
func waitIdle(t *testing.T, s *Service) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.Active() {
			return s.State()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("still active at %v", s.State())
	return State{}
}

const (
	press   = contracts.HotkeyPressed
	release = contracts.HotkeyReleased
	toggle  = contracts.HotkeyToggled
)

func TestDictation(t *testing.T) {
	unsupported := contracts.Unsupported(contracts.CapTextInsertion)
	tests := []struct {
		name string
		rec  fakeRecorder
		ins  fakeInserter
		busy bool
		// steps: a hotkey event, or a pause on the clock before the next.
		steps    []any
		inserted []string
		code     string
		started  int
	}{
		{name: "toggle on and off", rec: fakeRecorder{text: "  Hello, world.  "},
			steps: []any{toggle, toggle}, inserted: []string{"Hello, world."}, started: 1},
		{name: "tap leaves it on, next press stops", rec: fakeRecorder{text: "tap"},
			steps: []any{press, 100 * time.Millisecond, release, press, release}, inserted: []string{"tap"}, started: 1},
		{name: "hold stops on release", rec: fakeRecorder{text: "held"},
			steps: []any{press, time.Second, release}, inserted: []string{"held"}, started: 1},
		{name: "nothing heard", rec: fakeRecorder{text: " "},
			steps: []any{toggle, toggle}, code: CodeNoSpeech, started: 1},
		{name: "microphone fails", rec: fakeRecorder{startErr: ErrMicrophone},
			steps: []any{toggle}, code: CodeMicrophone, started: 1},
		{name: "recogniser fails to start", rec: fakeRecorder{startErr: errors.New("no model")},
			steps: []any{toggle}, code: CodeRecognition, started: 1},
		{name: "recogniser fails to decode", rec: fakeRecorder{finishErr: errors.New("crash")},
			steps: []any{toggle, toggle}, code: CodeRecognition, started: 1},
		{name: "cannot type here", rec: fakeRecorder{text: "x"}, ins: fakeInserter{err: unsupported},
			steps: []any{toggle, toggle}, inserted: []string{"x"}, code: CodeUnsupported, started: 1},
		{name: "consent declined", rec: fakeRecorder{text: "x"}, ins: fakeInserter{err: contracts.PermissionDenied(contracts.CapTextInsertion)},
			steps: []any{toggle, toggle}, inserted: []string{"x"}, code: CodeDenied, started: 1},
		{name: "typing broke", rec: fakeRecorder{text: "x"}, ins: fakeInserter{err: errors.New("pipe")},
			steps: []any{toggle, toggle}, inserted: []string{"x"}, code: CodeInsert, started: 1},
		{name: "talk has the microphone", busy: true, rec: fakeRecorder{text: "x"},
			steps: []any{toggle}, code: CodeBusy},
	}
	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			clk := &clock{t: time.Unix(1000, 0)}
			s := New(Config{Recorder: &tt.rec, Inserter: &tt.ins, Busy: func() bool { return tt.busy }})
			s.now = clk.now
			defer s.Close()
			// A start is Listening at once, so steps need no waiting.
			for _, st := range tt.steps {
				switch v := st.(type) {
				case time.Duration:
					clk.add(v)
				case contracts.HotkeyEventKind:
					s.Hotkey(v)
				}
			}
			st := waitIdle(t, s)
			if st.Phase != Idle || st.Err != tt.code {
				t.Errorf("state %+v, want idle with %q", st, tt.code)
			}
			if got := tt.ins.texts(); !slices.Equal(got, tt.inserted) {
				t.Errorf("inserted %q, want %q", got, tt.inserted)
			}
			if tt.rec.started != tt.started {
				t.Errorf("started %d times, want %d", tt.rec.started, tt.started)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	t.Run("while listening", func(t *testing.T) {
		rec := &fakeRecorder{text: "x"}
		ins := &fakeInserter{}
		s := New(Config{Recorder: rec, Inserter: ins})
		defer s.Close()
		if s.Cancel() {
			t.Error("Cancel with nothing in flight reported true")
		}
		s.Hotkey(toggle)
		waitPhase(t, s, Listening)
		if !s.Cancel() {
			t.Error("Cancel reported nothing to cancel")
		}
		if st := waitIdle(t, s); st.Err != "" {
			t.Errorf("a cancel shows %q", st.Err)
		}
		if rec.cancelled != 1 || rec.finished != 0 || len(ins.texts()) != 0 {
			t.Errorf("cancelled %d finished %d inserted %v", rec.cancelled, rec.finished, ins.texts())
		}
	})
	t.Run("while typing", func(t *testing.T) {
		ins := &fakeInserter{block: true}
		s := New(Config{Recorder: &fakeRecorder{text: "x"}, Inserter: ins})
		defer s.Close()
		s.Hotkey(toggle)
		waitPhase(t, s, Listening)
		s.Hotkey(toggle)
		waitPhase(t, s, Typing)
		s.Hotkey(toggle) // a press while typing changes nothing
		s.Cancel()
		if st := waitIdle(t, s); st.Err != "" {
			t.Errorf("a cancel shows %q", st.Err)
		}
	})
}

func TestMaxDuration(t *testing.T) {
	ins := &fakeInserter{}
	s := New(Config{Recorder: &fakeRecorder{text: "long"}, Inserter: ins, MaxDuration: 20 * time.Millisecond})
	defer s.Close()
	s.Hotkey(toggle)
	waitPhase(t, s, Listening)
	if st := waitIdle(t, s); st.Err != "" || !slices.Equal(ins.texts(), []string{"long"}) {
		t.Errorf("state %+v inserted %v", st, ins.texts())
	}
}

func TestWatchAndDismiss(t *testing.T) {
	s := New(Config{Recorder: &fakeRecorder{text: ""}, Inserter: &fakeInserter{}})
	defer s.Close()
	var mu sync.Mutex
	var phases []Phase
	stop := s.Watch(func() {
		st := s.State()
		mu.Lock()
		phases = append(phases, st.Phase)
		mu.Unlock()
	})
	s.Hotkey(toggle)
	waitPhase(t, s, Listening)
	s.Hotkey(toggle)
	if st := waitIdle(t, s); st.Err != CodeNoSpeech {
		t.Fatalf("state %+v", st)
	}
	s.Dismiss()
	if st := s.State(); st.Err != "" {
		t.Errorf("not dismissed: %+v", st)
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	// Watchers read the latest state, so fast phases may be skipped; every
	// change still notifies.
	if len(phases) < 3 || phases[len(phases)-1] != Idle {
		t.Errorf("watched phases %v", phases)
	}
}

func TestCloseEndsDictation(t *testing.T) {
	rec := &fakeRecorder{text: "x"}
	s := New(Config{Recorder: rec, Inserter: &fakeInserter{}})
	s.Hotkey(toggle)
	waitPhase(t, s, Listening)
	s.Close()
	if s.Active() || rec.cancelled != 1 {
		t.Errorf("active %v cancelled %d", s.Active(), rec.cancelled)
	}
	s.Hotkey(toggle)
	if s.Active() {
		t.Error("started after Close")
	}
}
