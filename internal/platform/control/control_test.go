//go:build linux

package control

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xlsft/companion/pkg/contracts"
)

func TestParseLine(t *testing.T) {
	tests := []struct {
		line string
		want Command
		code string
	}{
		{"ping", Command{Verb: VerbPing}, ""},
		{"hotkey talk press", Command{VerbHotkey, "talk", ActionPress}, ""},
		{"hotkey talk release", Command{VerbHotkey, "talk", ActionRelease}, ""},
		{"  hotkey   cancel  toggle ", Command{VerbHotkey, "cancel", ActionToggle}, ""},
		{"hotkey dictate_2 press", Command{VerbHotkey, "dictate_2", ActionPress}, ""},
		{"", Command{}, CodeBadCommand},
		{"ping now", Command{}, CodeBadCommand},
		{"hotkey talk", Command{}, CodeBadCommand},
		{"hotkey talk press extra", Command{}, CodeBadCommand},
		{"HOTKEY talk press", Command{}, CodeBadCommand},
		{"hotkey Talk press", Command{}, CodeBadID},
		{"hotkey ../x press", Command{}, CodeBadID},
		{"hotkey " + strings.Repeat("a", 33) + " press", Command{}, CodeBadID},
		{"hotkey talk hold", Command{}, CodeBadAction},
	}
	for _, tt := range tests {
		got, err := ParseLine(tt.line)
		code := ""
		var pe *ProtocolError
		if errors.As(err, &pe) {
			code = pe.Code
		} else if err != nil {
			code = "?" + err.Error()
		}
		if got != tt.want || code != tt.code {
			t.Errorf("ParseLine(%q) = %+v, %q; want %+v, %q", tt.line, got, code, tt.want, tt.code)
		}
	}
}

func TestCtlLine(t *testing.T) {
	for sub, want := range map[string]string{
		"talk-toggle": "hotkey talk toggle", "talk-press": "hotkey talk press",
		"talk-release": "hotkey talk release", "cancel": "hotkey cancel press", "ping": "ping",
	} {
		got, err := CtlLine(sub)
		if err != nil || got != want {
			t.Errorf("CtlLine(%q) = %q, %v", sub, got, err)
		}
		if _, err := ParseLine(got); err != nil {
			t.Errorf("CtlLine(%q) produced an invalid line: %v", sub, err)
		}
	}
	if _, err := CtlLine("talk"); !errors.Is(err, ErrUnknownCtl) {
		t.Errorf("unknown subcommand: %v", err)
	}
}

func listen(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openclick", "control.sock")
	s, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func nextEvent(t *testing.T, s *Server) contracts.HotkeyEvent {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return contracts.HotkeyEvent{}
}

func TestServer(t *testing.T) {
	s := listen(t)
	ctx := context.Background()
	st, err := os.Stat(s.Path())
	if err != nil || st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v, %v", st.Mode(), err)
	}
	if dst, _ := os.Stat(filepath.Dir(s.Path())); dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", dst.Mode())
	}

	if err := Ctl(ctx, s.Path(), "talk-toggle"); !isCode(err, CodeUnknownBinding) {
		t.Fatalf("before Register: %v", err)
	}
	if _, err := s.Register(ctx, []contracts.HotkeyBinding{{ID: TalkID}, {ID: CancelID}}); err != nil {
		t.Fatal(err)
	}
	if err := Ctl(ctx, s.Path(), "ping"); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		sub     string
		want    contracts.HotkeyEvent
		release bool // SupportsRelease after the step
	}{
		{"talk-toggle", contracts.HotkeyEvent{ID: TalkID, Kind: contracts.HotkeyToggled}, false},
		{"cancel", contracts.HotkeyEvent{ID: CancelID, Kind: contracts.HotkeyPressed}, false},
		{"talk-press", contracts.HotkeyEvent{ID: TalkID, Kind: contracts.HotkeyPressed}, false},
		{"talk-release", contracts.HotkeyEvent{ID: TalkID, Kind: contracts.HotkeyReleased}, true},
	}
	for _, st := range steps {
		if err := Ctl(ctx, s.Path(), st.sub); err != nil {
			t.Fatalf("%s: %v", st.sub, err)
		}
		if ev := nextEvent(t, s); ev != st.want {
			t.Errorf("%s: event %+v, want %+v", st.sub, ev, st.want)
		}
		if s.SupportsRelease() != st.release {
			t.Errorf("%s: SupportsRelease = %v", st.sub, s.SupportsRelease())
		}
	}

	// Several commands on one connection, and a malformed one in between.
	c, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hotkey talk press\nbogus\nhotkey talk release\n"))
	r := bufio.NewReader(c)
	for _, want := range []string{"ok\n", "err bad-command\n", "ok\n"} {
		if got, _ := r.ReadString('\n'); got != want {
			t.Errorf("reply %q, want %q", got, want)
		}
	}
	c.Write([]byte(strings.Repeat("x", maxLine+10) + "\n"))
	if got, _ := r.ReadString('\n'); got != "err too-long\n" {
		t.Errorf("long line reply %q", got)
	}

	if err := s.Unregister(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Ctl(ctx, s.Path(), "cancel"); !isCode(err, CodeUnknownBinding) {
		t.Errorf("after Unregister: %v", err)
	}
}

func isCode(err error, code string) bool {
	var pe *ProtocolError
	return errors.As(err, &pe) && pe.Code == code
}

func TestBusy(t *testing.T) {
	s := listen(t)
	ctx := context.Background()
	s.Register(ctx, []contracts.HotkeyBinding{{ID: TalkID}})
	var err error
	for i := 0; i < cap(s.events)+1 && err == nil; i++ {
		err = Ctl(ctx, s.Path(), "talk-toggle")
	}
	if !isCode(err, CodeBusy) {
		t.Errorf("full queue: %v", err)
	}
}

func TestListenSocketReuse(t *testing.T) {
	s := listen(t)
	if _, err := Listen(s.Path()); !errors.Is(err, ErrInUse) {
		t.Fatalf("second Listen: %v", err)
	}
	// A crashed process leaves the socket file behind without a listener.
	path := filepath.Join(t.TempDir(), "stale.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	if err := Send(context.Background(), path, "ping"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Send to stale socket: %v", err)
	}
	s2, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	if err := Send(context.Background(), path, "ping"); err != nil {
		t.Error(err)
	}
	s2.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket left after Close: %v", err)
	}
	if err := Send(context.Background(), path, "ping"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Send after Close: %v", err)
	}
}

func TestPrepareDirRejectsForeignOwner(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	if err := prepareDir(dir, uint32(os.Getuid())); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("mode not tightened: %v", st.Mode())
	}
	if err := prepareDir(dir, uint32(os.Getuid())+1); err == nil {
		t.Error("directory of another uid accepted")
	}
}

// socketPair returns both ends of a connected AF_UNIX stream pair.
func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conv := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "pair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	return conv(fds[0]), conv(fds[1])
}

func TestPeerCredentials(t *testing.T) {
	tests := []struct {
		name   string
		allow  uint32
		reply  string
		events int
	}{
		{"same uid accepted", uint32(os.Getuid()), "ok\n", 1},
		{"other uid refused", uint32(os.Getuid()) + 1, "err forbidden\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(tt.allow)
			s.Register(context.Background(), []contracts.HotkeyBinding{{ID: TalkID}})
			srv, cli := socketPair(t)
			defer cli.Close()
			done := make(chan struct{})
			go func() { s.serveConn(srv); close(done) }()
			cli.Write([]byte("hotkey talk toggle\n"))
			got, _ := bufio.NewReader(cli).ReadString('\n')
			if got != tt.reply {
				t.Errorf("reply %q, want %q", got, tt.reply)
			}
			cli.CloseWrite()
			<-done
			if len(s.events) != tt.events {
				t.Errorf("%d events queued, want %d", len(s.events), tt.events)
			}
		})
	}
}

func TestSendRejectsInjection(t *testing.T) {
	if err := Send(context.Background(), "/nonexistent", "ping\nhotkey talk press"); !isCode(err, CodeBadCommand) {
		t.Errorf("multi-line accepted: %v", err)
	}
}
