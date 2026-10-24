package keys

import (
	"testing"

	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/pkg/contracts"
)

func TestKeysym(t *testing.T) {
	tests := []struct {
		name string
		sym  uint32
		xkb  string
		ok   bool
	}{
		{"a", 0x61, "a", true},
		{"z", 0x7a, "z", true},
		{"0", 0x30, "0", true},
		{"space", 0x20, "space", true},
		{"escape", 0xff1b, "Escape", true},
		{"grave", 0x60, "grave", true},
		{"f1", 0xffbe, "F1", true},
		{"f12", 0xffc9, "F12", true},
		{"f24", 0xffd5, "F24", true},
		{"pageup", 0xff55, "Prior", true},
		{"f25", 0, "", false},
		{"A", 0, "", false}, // names are lower-case by contract
		{"", 0, "", false},
	}
	for _, tt := range tests {
		sym, ok := Keysym(tt.name)
		x, _ := XKBName(tt.name)
		if sym != tt.sym || ok != tt.ok || x != tt.xkb {
			t.Errorf("%q: got %#x %q %v, want %#x %q %v", tt.name, sym, x, ok, tt.sym, tt.xkb, tt.ok)
		}
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want contracts.Hotkey
		err  bool
	}{
		{"ctrl+alt+space", contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}, false},
		{"CTRL+ALT+space", contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}, false},
		{"Ctrl+Alt+Space", contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}, false},
		{"<Super>Escape", contracts.Hotkey{}, true},
		{"LOGO+Escape", contracts.Hotkey{Mods: contracts.ModSuper, Key: "escape"}, false},
		{"Shift+F10", contracts.Hotkey{Mods: contracts.ModShift, Key: "f10"}, false},
		{"escape", contracts.Hotkey{Key: "escape"}, false},
		{"Esc", contracts.Hotkey{Key: "escape"}, false},
		{"Page_Down", contracts.Hotkey{Key: "pagedown"}, false},
		{"ctrl+`", contracts.Hotkey{Mods: contracts.ModCtrl, Key: "grave"}, false},
		{"hyper+a", contracts.Hotkey{}, true},
		{"ctrl+", contracts.Hotkey{}, true},
		{"", contracts.Hotkey{}, true},
		{"ctrl+nosuchkey", contracts.Hotkey{}, true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if (err != nil) != tt.err || got != tt.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, err=%v", tt.in, got, err, tt.want, tt.err)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		h      contracts.Hotkey
		str    string
		portal string
	}{
		{contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}, "ctrl+alt+space", "CTRL+ALT+space"},
		{contracts.Hotkey{Mods: contracts.ModSuper | contracts.ModShift, Key: "f5"}, "shift+super+f5", "SHIFT+LOGO+F5"},
		{contracts.Hotkey{Key: "escape"}, "escape", "Escape"},
	}
	for _, tt := range tests {
		if s := String(tt.h); s != tt.str {
			t.Errorf("String(%+v) = %q, want %q", tt.h, s, tt.str)
		}
		p, err := PortalTrigger(tt.h)
		if err != nil || p != tt.portal {
			t.Errorf("PortalTrigger(%+v) = %q, %v; want %q", tt.h, p, err, tt.portal)
		}
		back, err := Parse(tt.str)
		if err != nil || back != tt.h {
			t.Errorf("Parse(String(%+v)) = %+v, %v", tt.h, back, err)
		}
	}
	if _, err := PortalTrigger(contracts.Hotkey{Key: "nope"}); err == nil {
		t.Error("PortalTrigger accepted an unknown key")
	}
}

// Every key a stored shortcut may name must have a keysym, or a backend
// could not bind what the settings accepted.
func TestShortcutKeysKnown(t *testing.T) {
	for _, k := range shortcut.Keys() {
		if _, ok := Keysym(k); !ok {
			t.Errorf("shortcut key %q has no keysym", k)
		}
	}
}
