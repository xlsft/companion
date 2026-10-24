// Command hotkeys is the OC-57 spike: it registers Talk and Cancel through
// the Linux hotkey chooser (or one forced backend) and prints every event
// with timestamps for a while.
//
//	hotkeys -for 30s                       # chooser; press the chords
//	hotkeys -drive bin/companion -for 5s   # also run "companion ctl" itself
//	DISPLAY=:97 hotkeys -backend x11 -xtest  # inject keys with XTEST (Xvfb)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/xlsft/companion/internal/platform/control"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/platform/hotkeys"
	"github.com/xlsft/companion/internal/platform/keybind"
	"github.com/xlsft/companion/internal/platform/keys"
	waylandhotkeys "github.com/xlsft/companion/internal/platform/linuxwayland/hotkeys"
	x11hotkeys "github.com/xlsft/companion/internal/platform/linuxx11/hotkeys"
	"github.com/xlsft/companion/pkg/contracts"
)

type service interface {
	contracts.HotkeyService
	Close() error
}

var start = time.Now()

func logf(format string, a ...any) {
	fmt.Printf("%s +%7.1fms "+format+"\n", append([]any{time.Now().Format("15:04:05.000"),
		float64(time.Since(start).Microseconds()) / 1000}, a...)...)
}

func main() {
	dur := flag.Duration("for", 20*time.Second, "how long to print events")
	backend := flag.String("backend", "auto", "auto (chooser), x11, portal or ipc")
	talkChord := flag.String("talk", "ctrl+alt+space", "Talk chord")
	socket := flag.String("socket", "", "control socket (default $XDG_RUNTIME_DIR/openclick/control.sock)")
	drive := flag.String("drive", "", "path to the companion binary: run its ctl commands against the socket")
	inject := flag.Bool("xtest", false, "x11: inject Talk (held 1 s, longer than autorepeat delay) and Escape with XTEST")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *dur, *backend, *talkChord, *socket, *drive, *inject); err != nil {
		logf("error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dur time.Duration, backend, talkChord, socket, drive string, inject bool) error {
	report, err := detect.New().Detect(ctx)
	if err != nil {
		logf("detect: %v", err)
	}
	logf("session=%s compositor=%q global_hotkey=%+v hotkey_release=%+v", report.Session, report.Compositor,
		report.State(contracts.CapGlobalHotkey), report.State(contracts.CapHotkeyRelease))

	talk, err := keys.Parse(talkChord)
	if err != nil {
		return err
	}
	// Escape alone is a fine cancel key where we grab it ourselves; through
	// a portal it would claim Escape for the whole desktop.
	cancel := contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "escape"}
	if report.Session == contracts.SessionX11 || backend == "x11" {
		cancel = contracts.Hotkey{Key: "escape"}
	}
	bindings := []contracts.HotkeyBinding{
		{ID: control.TalkID, Hotkey: talk, Description: "Talk to Companion"},
		{ID: control.CancelID, Hotkey: cancel, Description: "Cancel"},
	}
	if socket == "" {
		if socket, err = control.DefaultPath(); err != nil {
			return err
		}
	}

	var svc service
	name := backend
	t := time.Now()
	switch backend {
	case "auto":
		s, err := hotkeys.NewWithSocket(ctx, report, socket)
		if err != nil {
			return err
		}
		svc = s
		name = s.Backend()
	case "x11":
		svc, err = x11hotkeys.Open()
	case "portal":
		svc, err = waylandhotkeys.Open(ctx)
	case "ipc":
		svc, err = control.Listen(socket)
	default:
		return fmt.Errorf("unknown backend %q", backend)
	}
	if err != nil {
		return err
	}
	defer svc.Close()
	logf("backend %s opened in %v", name, time.Since(t).Round(time.Microsecond))

	t = time.Now()
	rctx, rcancel := context.WithTimeout(ctx, 60*time.Second) // portal dialog
	granted, err := svc.Register(rctx, bindings)
	rcancel()
	logf("Register in %v: err=%v", time.Since(t).Round(time.Microsecond), err)
	for _, b := range granted {
		logf("  granted %s = %s trigger=%q", b.ID, keys.String(b.Hotkey), b.TriggerDescription)
	}
	if s, ok := svc.(*hotkeys.Service); ok {
		logf("backend after Register: %s", s.Backend())
		if s.Backend() == hotkeys.BackendIPC {
			m := keybind.Resolve(report.Compositor, report.Desktop, false)
			talk, cancel := keybind.Commands("companion")
			logf("keybind method %s; by hand bind %q and %q", m, talk, cancel)
		}
	}
	logf("SupportsRelease=%v; listening %v", svc.SupportsRelease(), dur)

	if drive != "" {
		go driveCtl(ctx, drive, socket)
	}
	if inject {
		go func() {
			if err := injectX11(talk); err != nil {
				logf("xtest: %v", err)
			}
		}()
	}

	var pressed time.Time
	end := time.After(dur)
	for {
		select {
		case ev, ok := <-svc.Events():
			if !ok {
				return errors.New("events closed")
			}
			extra := ""
			switch ev.Kind {
			case contracts.HotkeyPressed:
				pressed = time.Now()
			case contracts.HotkeyReleased:
				if !pressed.IsZero() {
					extra = fmt.Sprintf(" held=%v", time.Since(pressed).Round(time.Millisecond))
				}
			}
			logf("event %-6s %-8s SupportsRelease=%v%s", ev.ID, kindName(ev.Kind), svc.SupportsRelease(), extra)
		case <-end:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func kindName(k contracts.HotkeyEventKind) string {
	switch k {
	case contracts.HotkeyPressed:
		return "pressed"
	case contracts.HotkeyReleased:
		return "released"
	case contracts.HotkeyToggled:
		return "toggled"
	}
	return "?"
}

// driveCtl plays a compositor: it runs "companion ctl" as a child process,
// like a keybinding would, and times each round trip.
func driveCtl(ctx context.Context, bin, socket string) {
	bin, _ = filepath.Abs(bin)
	steps := []struct {
		sub   string
		pause time.Duration
	}{
		{"ping", 300 * time.Millisecond},
		{"talk-toggle", 400 * time.Millisecond},
		{"talk-toggle", 200 * time.Millisecond},
		{"cancel", 200 * time.Millisecond},
		{"talk-press", 500 * time.Millisecond}, // Hyprland bind / Sway bindsym
		{"talk-release", 200 * time.Millisecond},
		{"talk-toggle", 0},
	}
	for _, st := range steps {
		select {
		case <-ctx.Done():
			return
		case <-time.After(st.pause):
		}
		t := time.Now()
		out, err := exec.CommandContext(ctx, bin, "ctl", "-socket", socket, st.sub).CombinedOutput()
		logf("drive: companion ctl %-12s exit=%v in %v %s", st.sub, err, time.Since(t).Round(time.Microsecond), out)
	}
	t := time.Now()
	err := control.Ctl(ctx, socket, "talk-toggle")
	logf("drive: in-process Ctl talk-toggle err=%v in %v", err, time.Since(t).Round(time.Microsecond))
}

// injectX11 presses the Talk chord for 1 s and then Escape through XTEST.
func injectX11(talk contracts.Hotkey) error {
	c, err := xgb.NewConn()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := xtest.Init(c); err != nil {
		return err
	}
	setup := xproto.Setup(c)
	root := setup.DefaultScreen(c).Root
	km, err := xproto.GetKeyboardMapping(c, setup.MinKeycode, byte(int(setup.MaxKeycode)-int(setup.MinKeycode)+1)).Reply()
	if err != nil {
		return err
	}
	code := func(sym uint32) byte {
		for i := 0; i+int(km.KeysymsPerKeycode) <= len(km.Keysyms); i += int(km.KeysymsPerKeycode) {
			if uint32(km.Keysyms[i]) == sym {
				return byte(int(setup.MinKeycode) + i/int(km.KeysymsPerKeycode))
			}
		}
		return 0
	}
	key := func(press bool, c2 byte) {
		typ := byte(xproto.KeyRelease)
		if press {
			typ = xproto.KeyPress
		}
		_ = xtest.FakeInputChecked(c, typ, c2, 0, root, 0, 0, 0).Check()
	}
	var mods []byte
	for _, m := range []struct {
		m   contracts.Modifier
		sym uint32
	}{{contracts.ModCtrl, 0xffe3}, {contracts.ModAlt, 0xffe9}, {contracts.ModShift, 0xffe1}, {contracts.ModSuper, 0xffeb}} {
		if talk.Mods&m.m != 0 {
			mods = append(mods, code(m.sym))
		}
	}
	sym, _ := keys.Keysym(talk.Key)
	time.Sleep(500 * time.Millisecond)
	logf("xtest: holding %s for 1s", keys.String(talk))
	for _, m := range mods {
		key(true, m)
	}
	key(true, code(sym))
	time.Sleep(time.Second)
	key(false, code(sym))
	for _, m := range mods {
		key(false, m)
	}
	time.Sleep(300 * time.Millisecond)
	logf("xtest: tapping escape")
	key(true, code(0xff1b))
	key(false, code(0xff1b))
	return nil
}
