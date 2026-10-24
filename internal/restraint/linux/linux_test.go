//go:build linux

package linux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func load(t *testing.T, name string) *graph {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := parsePWDump(b)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestPWDumpScreenAndMic(t *testing.T) {
	const own = 777
	tests := []struct {
		fixture string
		anyMic  bool
		screen  int
		mic     int
	}{
		{"idle", false, 0, 0},
		{"call", false, 0, 1},            // pid resolved via client object
		{"dictation", false, 0, 0},       // unknown app, not Communication role
		{"dictation", true, 0, 1},        // ... unless any mic counts
		{"own_mic", false, 0, 0},         // our own pid excluded
		{"paused_call", false, 0, 0},     // not running
		{"screenshare", false, 1, 0},     // portal screencast consumed by chromium
		{"camera", false, 0, 0},          // camera is not a screen share
		{"screenshare_own", false, 0, 0}, // our own consumer excluded
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			g := load(t, tt.fixture)
			if got := len(g.screenShares(own)); got != tt.screen {
				t.Errorf("screen shares = %d, want %d", got, tt.screen)
			}
			if got := len(g.micUsers(own, tt.anyMic)); got != tt.mic {
				t.Errorf("mic users = %d, want %d", got, tt.mic)
			}
		})
	}
}

func TestCallPIDFromClient(t *testing.T) {
	c := load(t, "call").micUsers(1, false)
	if len(c) != 1 || c[0].PID != 5100 || c[0].Detail != "zoom" {
		t.Fatalf("%+v", c)
	}
}

func TestParseBadJSON(t *testing.T) {
	if _, err := parsePWDump([]byte("nope")); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseNiriFullscreen(t *testing.T) {
	out := []byte(`{"logical":{"width":2560,"height":1080}}`)
	tests := []struct {
		name    string
		window  string
		output  []byte
		want    bool
		wantErr bool
	}{
		{"flag true", `{"is_fullscreen":true}`, nil, true, false},
		{"flag false", `{"is_fullscreen":false}`, nil, false, false},
		{"size fills output", `{"layout":{"window_size":[2560,1080]}}`, out, true, false},
		{"size smaller", `{"layout":{"window_size":[2536,1026]}}`, out, false, false},
		{"no info", `{"layout":{}}`, nil, false, true},
		{"garbage", `x`, nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNiriFullscreen([]byte(tt.window), tt.output)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %v, %v", got, err)
			}
			if err != nil && !errors.Is(err, ErrUnknown) {
				t.Fatalf("error not ErrUnknown: %v", err)
			}
		})
	}
}

func TestPWSignalsShareOneDump(t *testing.T) {
	calls := 0
	src := &pwSource{run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls++
		return os.ReadFile("testdata/screenshare.json")
	}}
	s := &pwSignal{name: NameScreenShare, src: src, pick: func(g *graph) int { return len(g.screenShares(1)) }}
	c := &pwSignal{name: NameCall, src: src, pick: func(g *graph) int { return len(g.micUsers(1, false)) }}
	a, err := s.Active(context.Background())
	if err != nil || !a {
		t.Fatalf("screen %v %v", a, err)
	}
	a, err = c.Active(context.Background())
	if err != nil || a {
		t.Fatalf("call %v %v", a, err)
	}
	if calls != 1 {
		t.Fatalf("pw-dump ran %d times", calls)
	}
}

func TestPWSignalMissingBinaryIsUnknown(t *testing.T) {
	src := &pwSource{run: func(context.Context, string, ...string) ([]byte, error) { return nil, ErrUnknown }}
	s := &pwSignal{name: NameCall, src: src, pick: func(*graph) int { return 1 }}
	a, err := s.Active(context.Background())
	if a || !errors.Is(err, ErrUnknown) {
		t.Fatalf("%v %v", a, err)
	}
}

func TestPresentationSignal(t *testing.T) {
	niri := func(out string) runner {
		return func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "focused-window" {
				return []byte(out), nil
			}
			return nil, ErrUnknown
		}
	}
	tests := []struct {
		name  string
		inh   bool
		inhEr error
		niri  bool
		win   string
		want  bool
	}{
		{"inhibited", true, nil, false, "", true},
		{"niri fullscreen", false, ErrUnknown, true, `{"is_fullscreen":true}`, true},
		{"niri windowed", false, nil, true, `{"is_fullscreen":false}`, false},
		{"niri nothing focused", false, nil, true, `null`, false},
		{"no sources", false, ErrUnknown, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &presentationSignal{
				run:            niri(tt.win),
				notifInhibited: func(context.Context) (bool, error) { return tt.inh, tt.inhEr },
				niri:           tt.niri,
			}
			got, _ := s.Active(context.Background())
			if got != tt.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestSignalsConstruct(t *testing.T) {
	names := map[string]bool{}
	for _, s := range Signals(Options{}) {
		names[s.Name()] = true
	}
	for _, n := range []string{NameScreenShare, NameCall, NamePresentation} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := execRunner(ctx, "definitely-not-a-binary-xyz")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("%v", err)
	}
}
