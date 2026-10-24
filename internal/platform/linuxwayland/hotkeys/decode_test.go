package hotkeys

import (
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/openclick/pkg/contracts"
)

var talk = contracts.HotkeyBinding{ID: "talk", Hotkey: contracts.Hotkey{Mods: contracts.ModCtrl | contracts.ModAlt, Key: "space"}, Description: "Talk"}

func TestShortcutsArg(t *testing.T) {
	arg, err := shortcutsArg([]contracts.HotkeyBinding{talk, {ID: "cancel", Hotkey: contracts.Hotkey{Key: "escape"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(arg) != 2 || arg[0].ID != "talk" || arg[0].Opts["preferred_trigger"].Value() != "CTRL+ALT+space" ||
		arg[0].Opts["description"].Value() != "Talk" || arg[1].Opts["preferred_trigger"].Value() != "Escape" ||
		arg[1].Opts["description"].Value() != "cancel" {
		t.Errorf("arg = %+v", arg)
	}
	if _, err := shortcutsArg([]contracts.HotkeyBinding{{ID: "x", Hotkey: contracts.Hotkey{Key: "nope"}}}); err == nil {
		t.Error("unknown key accepted")
	}
}

func opts(kv ...string) map[string]dbus.Variant {
	m := map[string]dbus.Variant{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = dbus.MakeVariant(kv[i+1])
	}
	return m
}

func TestDecodeShortcuts(t *testing.T) {
	want := []shortcutInfo{{ID: "talk", Description: "Talk", Trigger: "Ctrl+Alt+Space"}, {ID: "cancel"}}
	tests := []struct {
		name string
		in   any
		want []shortcutInfo
	}{
		{"variant of [][]any", dbus.MakeVariant([][]any{
			{"talk", opts("description", "Talk", "trigger_description", "Ctrl+Alt+Space")},
			{"cancel", opts()},
		}), want},
		{"bare [][]any", [][]any{
			{"talk", opts("description", "Talk", "trigger_description", "Ctrl+Alt+Space")},
			{"cancel", opts()},
		}, want},
		{"typed", []shortcut{{"talk", opts("description", "Talk", "trigger_description", "Ctrl+Alt+Space")}, {"cancel", opts()}}, want},
		{"malformed element skipped", [][]any{{"talk"}, {1, opts()}, {"cancel", opts()}}, []shortcutInfo{{ID: "cancel"}}},
		{"wrong type", "nope", nil},
		{"absent", nil, nil},
	}
	for _, tt := range tests {
		if got := decodeShortcuts(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestGranted(t *testing.T) {
	cancel := contracts.HotkeyBinding{ID: "cancel", Hotkey: contracts.Hotkey{Key: "escape"}}
	got := granted([]contracts.HotkeyBinding{talk, cancel}, []shortcutInfo{
		{ID: "talk", Trigger: "Super+F5"},   // user picked another chord
		{ID: "stranger", Trigger: "Ctrl+A"}, // not ours: ignored
	})
	want := []contracts.HotkeyBinding{{ID: "talk", Description: "Talk", TriggerDescription: "Super+F5",
		Hotkey: contracts.Hotkey{Mods: contracts.ModSuper, Key: "f5"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// A description we cannot parse keeps the requested chord.
	got = granted([]contracts.HotkeyBinding{talk}, []shortcutInfo{{ID: "talk", Trigger: "Press Hyper twice"}})
	if len(got) != 1 || got[0].Hotkey != talk.Hotkey || got[0].TriggerDescription != "Press Hyper twice" {
		t.Errorf("got %+v", got)
	}
}

func TestActivation(t *testing.T) {
	sess := dbus.ObjectPath("/org/freedesktop/portal/desktop/session/1_42/openclick_x")
	tests := []struct {
		body []any
		id   string
		ok   bool
	}{
		{[]any{sess, "talk", uint64(123), map[string]dbus.Variant{}}, "talk", true},
		{[]any{sess, "talk"}, "talk", true},
		{[]any{string(sess), "talk"}, "", false},
		{[]any{sess}, "", false},
		{nil, "", false},
	}
	for _, tt := range tests {
		gs, id, ok := activation(tt.body)
		if ok != tt.ok || (ok && (id != tt.id || gs != sess)) {
			t.Errorf("activation(%v) = %q %q %v", tt.body, gs, id, ok)
		}
	}
	gs, infos, ok := shortcutsChanged([]any{sess, [][]any{{"talk", opts("trigger_description", "Ctrl+Space")}}})
	if !ok || gs != sess || len(infos) != 1 || infos[0].Trigger != "Ctrl+Space" {
		t.Errorf("shortcutsChanged = %q %+v %v", gs, infos, ok)
	}
	if _, _, ok := shortcutsChanged([]any{"x"}); ok {
		t.Error("short body accepted")
	}
}
