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
			time.Sleep(1500 * time.Millisecond) // window mapped and focused
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
