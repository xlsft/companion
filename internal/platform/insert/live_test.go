//go:build live && linux

// The live test types into a terminal window it opens itself and reads
// back what arrived:
//
//	go test -tags=novulkan,live -run TestLiveInsert -v ./internal/platform/insert
//
// It needs a desktop session and kitty; focus must follow the new window.
package insert

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/platform/detect"
)

func TestLiveInsert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	report, _ := detect.New().Detect(ctx)
	want := "Hello, world! Привет, 你好 – ü\t42"
	for _, tc := range []struct {
		name string
		clip bool
	}{{"typed", false}, {"pasted", true}} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "typed.txt")
			term := exec.Command("kitty", "--class", "companion-insert-test", "sh", "-c", "stty -echo; cat > "+out)
			if err := term.Start(); err != nil {
				t.Skip("kitty:", err)
			}
			defer func() { _ = term.Process.Kill(); _ = term.Wait() }()
			if !focusedOn(ctx, "companion-insert-test", 5*time.Second) {
				t.Skip("the test window did not get keyboard focus; not typing into another window")
			}
			in := New(report, Options{UseClipboard: func() bool { return tc.clip }})
			defer in.Close()
			text := want + "\n"
			var spy *spyClip
			if tc.clip {
				// Through the paste path for every backend.
				for i, ty := range in.Typers {
					in.Typers[i] = unfaithful{ty}
				}
				spy = &spyClip{Clipboard: in.Clipboard}
				in.Clipboard = spy
			}
			if err := in.InsertText(ctx, text); err != nil {
				t.Fatalf("insert: %v", err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var got string
			for time.Now().Before(deadline) {
				b, _ := os.ReadFile(out)
				if got = string(b); strings.HasSuffix(got, "\n") {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if got != text {
				t.Errorf("typed %q, want %q", got, text)
			}
			if spy != nil && (spy.set != 1 || spy.restored != 1) {
				t.Errorf("clipboard set %d, restored %d times; want once each", spy.set, spy.restored)
			}
		})
	}
}

// unfaithful forces the clipboard path.
type unfaithful struct{ Typer }

func (unfaithful) Faithful(string) bool { return false }

type spyClip struct {
	Clipboard
	set, restored int
}

func (s *spyClip) SetText(ctx context.Context, text string) error {
	s.set++
	return s.Clipboard.SetText(ctx, text)
}

func (s *spyClip) Restore(ctx context.Context, c Clip) error {
	s.restored++
	return s.Clipboard.Restore(ctx, c)
}

// focusedOn waits until the window with appID has keyboard focus, so a
// live test never types into someone else's window. Only niri can tell;
// elsewhere it trusts that a new window takes focus.
func focusedOn(ctx context.Context, appID string, wait time.Duration) bool {
	if os.Getenv("NIRI_SOCKET") == "" {
		time.Sleep(1500 * time.Millisecond)
		return true
	}
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		out, err := exec.CommandContext(ctx, "niri", "msg", "--json", "focused-window").Output()
		var w struct {
			AppID string `json:"app_id"`
		}
		if err == nil && json.Unmarshal(out, &w) == nil && w.AppID == appID {
			time.Sleep(300 * time.Millisecond) // let the client settle its keymap
			return true
		}
	}
	return false
}
