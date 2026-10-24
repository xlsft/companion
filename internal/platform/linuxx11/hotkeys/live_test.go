package hotkeys

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/xlsft/openclick/pkg/contracts"
)

// TestLive drives a real X server with XTEST. It needs $DISPLAY and is
// opt-in because injected keys reach whatever has focus there. Use Xvfb:
// Xwayland 24 hands XTEST input to the compositor (libei) instead of
// processing it, and Ctrl+Alt+F<n> is eaten by XKB as a VT switch.
func TestLive(t *testing.T) {
	if os.Getenv("OPENCLICK_X11_LIVE") == "" || os.Getenv("DISPLAY") == "" {
		t.Skip("set OPENCLICK_X11_LIVE=1 with a DISPLAY to run")
	}
	ctx := context.Background()
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	talk := contracts.HotkeyBinding{ID: "talk", Hotkey: contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}}
	if _, err := s.Register(ctx, []contracts.HotkeyBinding{talk}); err != nil {
		t.Fatal(err)
	}

	other, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Register(ctx, []contracts.HotkeyBinding{talk})
	if !errors.Is(err, contracts.ErrHotkeyConflict) {
		t.Fatalf("second grab: err = %v, want conflict", err)
	}
	// Its bindings would be retried on the next MappingNotify.
	other.Close()

	inj, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	if err := xtest.Init(inj); err != nil {
		t.Skip("XTEST unavailable:", err)
	}
	// A passive grab activates only if the focus is the root or below it;
	// rootless Xwayland leaves focus at None while no X client is focused.
	var focus xproto.Window
	if r, err := xproto.GetInputFocus(inj).Reply(); err == nil {
		focus = r.Focus
		t.Logf("input focus before test: %#x", focus)
	}
	if focus == xproto.InputFocusNone {
		_ = xproto.SetInputFocusChecked(inj, xproto.InputFocusPointerRoot, xproto.InputFocusPointerRoot, xproto.TimeCurrentTime).Check()
	}
	code := func(sym uint32) byte { return byte(s.km.keycodes(sym)[0]) }
	ctrl, alt, space := code(0xffe3), code(0xffe9), code(0x20)
	key := func(press bool, c byte) {
		typ := byte(xproto.KeyRelease)
		if press {
			typ = xproto.KeyPress
		}
		if err := xtest.FakeInputChecked(inj, typ, c, 0, s.root, 0, 0, 0).Check(); err != nil {
			t.Fatal(err)
		}
	}
	key(true, ctrl)
	key(true, alt)
	key(true, space)
	// Longer than the default 660 ms repeat delay, so a server that
	// autorepeats injected keys sends repeat pairs that must be filtered.
	time.Sleep(time.Second)
	key(false, space)
	key(false, alt)
	key(false, ctrl)

	want := []contracts.HotkeyEventKind{contracts.HotkeyPressed, contracts.HotkeyReleased}
	for _, k := range want {
		select {
		case ev := <-s.Events():
			if ev.ID != "talk" || ev.Kind != k {
				t.Fatalf("event %+v, want talk kind %d", ev, k)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no event kind %d", k)
		}
	}
	select {
	case ev := <-s.Events():
		t.Fatalf("extra event %+v (autorepeat leaked)", ev)
	case <-time.After(100 * time.Millisecond):
	}
}
