package wizard

import (
	"context"
	"testing"
	"time"
)

type fakeDictate struct{ code string }

func (fakeDictate) Hotkeys() HotkeyStatus {
	return HotkeyStatus{Talk: "super+space", Cancel: "super+backspace", Dictate: "ctrl+alt+space", DictateState: HotkeyApplied}
}

func (f fakeDictate) ApplyDictateHotkey(_ context.Context, chord string) HotkeyStatus {
	st := f.Hotkeys()
	st.Dictate = chord
	if f.code != "" {
		st.DictateState, st.DictateCode = HotkeyFailed, f.code
	}
	return st
}

func TestDictateEditorValidate(t *testing.T) {
	e := &DictateEditor{}
	st := fakeDictate{}.Hotkeys()
	for chord, code := range map[string]string{
		"ctrl+alt+space": "", "alt+super+d": "", "super+space": "same_as_other", "super+backspace": "same_as_other",
		"f8": "needs_modifier", "ctrl+c": "reserved", "": "empty",
	} {
		e.rec.Set(chord)
		if got := e.validate(st); got != code {
			t.Errorf("%q: code %q, want %q", chord, got, code)
		}
	}
}

// A refused apply stays on screen instead of the backend's stored state.
func TestDictateEditorKeepsFailure(t *testing.T) {
	for _, code := range []string{"", "hotkey_conflict"} {
		e := &DictateEditor{Backend: fakeDictate{code: code}}
		e.startApply("ctrl+alt+d")
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, applying := e.status(); !applying {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("apply never finished")
			}
			time.Sleep(time.Millisecond)
		}
		st, _ := e.status()
		switch {
		case code == "" && (st.DictateState != HotkeyApplied || st.Dictate != "ctrl+alt+space"):
			t.Errorf("success shows %+v, want the backend's state", st)
		case code != "" && (st.DictateState != HotkeyFailed || st.DictateCode != code):
			t.Errorf("failure shows %+v", st)
		}
	}
}
