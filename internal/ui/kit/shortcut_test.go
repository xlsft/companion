package kit

import (
	"testing"

	"gioui.org/io/key"
)

func TestShortcutRecorderPress(t *testing.T) {
	tests := []struct {
		name  string
		keys  []key.Event
		want  string
		still bool // still recording
	}{
		{"super space", []key.Event{{Name: key.NameSpace, Modifiers: key.ModSuper, State: key.Press}}, "super+space", false},
		{"cmd is super", []key.Event{{Name: "K", Modifiers: key.ModCommand | key.ModShift, State: key.Press}}, "shift+super+k", false},
		{"escape is a key", []key.Event{{Name: key.NameEscape, Modifiers: key.ModCtrl, State: key.Press}}, "ctrl+escape", false},
		{"backspace", []key.Event{{Name: key.NameDeleteBackward, Modifiers: key.ModSuper, State: key.Press}}, "super+backspace", false},
		{"f-key", []key.Event{{Name: key.NameF9, State: key.Press}}, "f9", false},
		{"punctuation", []key.Event{{Name: "/", Modifiers: key.ModAlt, State: key.Press}}, "alt+slash", false},
		{"modifiers alone wait", []key.Event{{Name: key.NameCtrl, State: key.Press}, {Name: key.NameAlt, Modifiers: key.ModCtrl, State: key.Press}}, "old", true},
		{"unknown key ignored", []key.Event{{Name: "§", Modifiers: key.ModSuper, State: key.Press}}, "old", true},
		{"release ignored", []key.Event{{Name: "A", Modifiers: key.ModSuper, State: key.Release}}, "old", true},
	}
	for _, tt := range tests {
		r := &ShortcutRecorder{value: "old"}
		r.Record()
		for _, e := range tt.keys {
			r.press(e)
		}
		if r.Value() != tt.want || r.Recording() != tt.still {
			t.Errorf("%s: value %q recording %v", tt.name, r.Value(), r.Recording())
		}
	}
	r := &ShortcutRecorder{}
	r.Record()
	r.press(key.Event{Name: key.NameCtrl, State: key.Press})
	r.press(key.Event{Name: key.NameShift, Modifiers: key.ModCtrl, State: key.Press})
	if r.held != modsOf(key.ModCtrl|key.ModShift) {
		t.Errorf("held = %v", r.held)
	}
	r.Set("ctrl+a")
	if r.Value() != "" {
		t.Error("Set overwrote a recording in progress")
	}
}
