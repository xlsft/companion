package wizard

import (
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/xlsft/companion/internal/readiness"
)

func needsBinding() readiness.Result {
	return readiness.Result{Status: readiness.Failed, Code: readiness.PermNeedsBinding,
		Detail: map[string]string{"microphone": readiness.PermGranted, "screen_capture": readiness.PermGranted,
			"global_hotkey": readiness.PermNeedsBinding, "overlay_click_through": readiness.PermGranted}}
}

func TestHotkeyEditorRecordsAndApplies(t *testing.T) {
	h := newHarness(t)
	h.checks.set(readiness.StepPermissions, needsBinding())
	h.frame()
	h.click(ActCheck)
	h.eventually("needs binding", func() bool { return h.idle() && h.shows("Action needed") })
	if h.shows("niri") || h.shows("binds {") {
		t.Fatal("a config snippet is on screen")
	}

	talk, _ := h.w.perm.hotkeys.Recorders()
	talk.Record()
	h.frame()
	if !h.shows("Press the keys") {
		t.Error("recording prompt missing")
	}
	talk.Press(key.Name("K"), key.ModCtrl|key.ModAlt)
	h.frame()
	h.frame()
	if talk.Value() != "ctrl+alt+k" {
		t.Fatalf("recorded %q", talk.Value())
	}
	if !h.shows("Not applied yet") || !h.shows("Ctrl") || !h.shows("Alt") {
		t.Error("draft state not shown")
	}

	// Applying passes the check on its own: no test press needed.
	h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Passed})
	h.click(ActApplyHotkeys)
	h.eventually("check re-ran", func() bool { return h.view() == readiness.StepBYOK })
	h.perm.mu.Lock()
	got, n := h.perm.st, h.perm.applies
	h.perm.mu.Unlock()
	if n != 1 || got.Talk != "ctrl+alt+k" || got.Cancel != "super+backspace" {
		t.Errorf("applied %d times: %+v", n, got)
	}
}

func TestHotkeyEditorValidation(t *testing.T) {
	tests := []struct {
		name  string
		key   key.Name
		mods  key.Modifiers
		want  string
		apply bool
	}{
		{"no modifier", key.NameSpace, 0, "Add Ctrl, Alt or Super", false},
		{"reserved", key.Name("C"), key.ModCtrl, "Reserved by the system", false},
		{"shift only", key.Name("A"), key.ModShift, "Shift alone types text", false},
		{"fine", key.NameF5, key.ModSuper, "Not applied yet", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.frame()
			talk, _ := h.w.perm.hotkeys.Recorders()
			talk.Record()
			talk.Press(tt.key, tt.mods)
			h.frame()
			h.frame()
			if !h.shows(tt.want) {
				t.Errorf("missing %q in %v", tt.want, h.w.frame.texts)
			}
			applies := func() int {
				h.perm.mu.Lock()
				defer h.perm.mu.Unlock()
				return h.perm.applies
			}
			h.click(ActApplyHotkeys)
			if tt.apply {
				h.eventually("apply", func() bool { return applies() == 1 })
				return
			}
			h.frame()
			if n := applies(); n != 0 {
				t.Errorf("invalid chord applied %d times", n)
			}
		})
	}
}

func TestHotkeyEditorOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		result HotkeyStatus
		want   []string
	}{
		{"applied toggle-only", HotkeyStatus{State: HotkeyApplied, ToggleOnly: true}, []string{"Works: Super+Space to talk", "press again to send"}},
		{"conflict", HotkeyStatus{State: HotkeyFailed, Code: "hotkey_conflict"}, []string{"Already used by something else"}},
		{"invalid config", HotkeyStatus{State: HotkeyFailed, Code: "config_invalid"}, []string{"nothing was changed"}},
		{"unknown code", HotkeyStatus{State: HotkeyFailed, Code: "weird"}, []string{"Could not apply (weird)"}},
		{"manual", HotkeyStatus{State: HotkeyManual, Commands: []string{"/usr/bin/companion ctl talk-toggle", "/usr/bin/companion ctl cancel"}},
			[]string{"cannot be set up automatically", "/usr/bin/companion ctl talk-toggle"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.perm.result = tt.result
			h.frame()
			h.click(ActApplyHotkeys)
			h.eventually("outcome", func() bool { return h.shows(tt.want[0]) })
			for _, w := range tt.want {
				if !h.shows(w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, s := range h.w.frame.texts {
				if strings.Contains(strings.ToLower(s), "config.kdl") {
					t.Errorf("file name on screen: %q", s)
				}
			}
		})
	}
}
