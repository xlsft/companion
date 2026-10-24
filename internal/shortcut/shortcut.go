// Package shortcut is the portable form of the user's global shortcuts
// (Talk, Cancel): the chord syntax stored in settings ("super+space"),
// its validation and its keycap labels. It knows nothing about how a
// platform binds a chord; internal/platform/keys and keybind translate
// it per backend.
package shortcut

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/xlsft/companion/pkg/contracts"
)

// Defaults. Cancel is not plain Escape: a global grab of Escape would
// take it away from every other app, and Super+Escape is taken by the
// shortcut-inhibit toggle on several desktops.
const (
	DefaultTalk   = "super+space"
	DefaultCancel = "super+backspace"
)

// Role is which shortcut a chord is for; Talk is stricter.
type Role int

const (
	Talk Role = iota
	Cancel
)

// keyNames are the key names a chord may end in, the same set
// internal/platform/keys maps to keysyms.
var keyNames = func() []string {
	n := []string{"space", "escape", "grave", "tab", "return", "backspace", "delete", "insert",
		"home", "end", "pageup", "pagedown", "left", "up", "right", "down",
		"minus", "equal", "comma", "period", "slash", "backslash", "semicolon", "apostrophe",
		"bracketleft", "bracketright", "pause", "print", "menu"}
	for c := 'a'; c <= 'z'; c++ {
		n = append(n, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		n = append(n, string(c))
	}
	for i := 1; i <= 24; i++ {
		n = append(n, "f"+strconv.Itoa(i))
	}
	return n
}()

// Keys lists the supported key names.
func Keys() []string { return slices.Clone(keyNames) }

var aliases = map[string]string{
	"esc": "escape", "enter": "return", "del": "delete", "ins": "insert",
	"prior": "pageup", "next": "pagedown", "page_up": "pageup", "page_down": "pagedown",
}

var modNames = map[string]contracts.Modifier{
	"ctrl": contracts.ModCtrl, "control": contracts.ModCtrl,
	"alt":   contracts.ModAlt,
	"shift": contracts.ModShift,
	"super": contracts.ModSuper, "meta": contracts.ModSuper, "win": contracts.ModSuper, "cmd": contracts.ModSuper,
}

var modOrder = []struct {
	m    contracts.Modifier
	name string
}{
	{contracts.ModCtrl, "ctrl"}, {contracts.ModAlt, "alt"}, {contracts.ModShift, "shift"}, {contracts.ModSuper, "super"},
}

// Error codes; the UI maps each to a message.
const (
	CodeEmpty      = "empty"
	CodeUnknownKey = "unknown_key"
	CodeNoModifier = "needs_modifier"
	CodeShiftOnly  = "shift_only"
	CodeReserved   = "reserved"
	CodeSame       = "same_as_talk"
)

// Error is a rejected chord.
type Error struct{ Code string }

func (e *Error) Error() string { return "shortcut: " + e.Code }

// Code returns the Error code of err, or "" when err is not an *Error.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Parse reads "ctrl+alt+space"; case and modifier order do not matter.
func Parse(s string) (contracts.Hotkey, error) {
	var h contracts.Hotkey
	s = strings.TrimSpace(s)
	if s == "" {
		return h, &Error{CodeEmpty}
	}
	parts := strings.Split(strings.ToLower(s), "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if i < len(parts)-1 {
			m, ok := modNames[p]
			if !ok {
				return contracts.Hotkey{}, &Error{CodeUnknownKey}
			}
			h.Mods |= m
			continue
		}
		if a, ok := aliases[p]; ok {
			p = a
		}
		if !slices.Contains(keyNames, p) {
			return contracts.Hotkey{}, &Error{CodeUnknownKey}
		}
		h.Key = p
	}
	return h, nil
}

// Format writes h in the stored syntax, modifiers in a fixed order.
func Format(h contracts.Hotkey) string {
	var b strings.Builder
	for _, m := range modOrder {
		if h.Mods&m.m != 0 {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(h.Key)
	return b.String()
}

// bareOK are keys that type nothing, so taking them globally without a
// modifier does not break typing in other apps.
func bareOK(k string) bool {
	if k == "pause" {
		return true
	}
	if n, ok := strings.CutPrefix(k, "f"); ok {
		_, err := strconv.Atoi(n)
		return err == nil
	}
	return false
}

// reserved are chords the system or every app already relies on; taking
// them globally would break something the user expects to work.
var reserved = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range []string{
		"ctrl+alt+delete", "ctrl+alt+backspace", "alt+tab", "alt+shift+tab", "ctrl+alt+tab",
		"super+tab", "super+shift+tab", "alt+f4", "ctrl+escape", "ctrl+shift+escape", "super+l", "print",
	} {
		m[c] = true
	}
	// Editing and window keys of every app.
	for _, k := range "acvxzysfwqtnpor" {
		m["ctrl+"+string(k)] = true
	}
	m["ctrl+shift+z"] = true
	for i := 1; i <= 12; i++ {
		m["ctrl+alt+f"+strconv.Itoa(i)] = true // virtual terminals
	}
	return m
}()

// Validate checks h for role; talk is the Talk chord when validating
// Cancel (they must differ), otherwise ignored.
func Validate(role Role, h, talk contracts.Hotkey) error {
	switch {
	case h.Key == "":
		return &Error{CodeEmpty}
	case !slices.Contains(keyNames, h.Key):
		return &Error{CodeUnknownKey}
	case reserved[Format(h)]:
		return &Error{CodeReserved}
	case h.Mods == 0 && (role == Talk || !bareOK(h.Key)):
		return &Error{CodeNoModifier}
	case h.Mods == contracts.ModShift && !bareOK(h.Key):
		return &Error{CodeShiftOnly}
	case role == Cancel && h == talk:
		return &Error{CodeSame}
	}
	return nil
}

// ValidatePair parses and checks both stored chords.
func ValidatePair(talk, cancel string) (contracts.Hotkey, contracts.Hotkey, error) {
	t, err := Parse(talk)
	if err == nil {
		err = Validate(Talk, t, contracts.Hotkey{})
	}
	if err != nil {
		return t, contracts.Hotkey{}, err
	}
	c, err := Parse(cancel)
	if err == nil {
		err = Validate(Cancel, c, t)
	}
	return t, c, err
}

var keyLabels = map[string]string{
	"space": "Space", "escape": "Esc", "grave": "`", "tab": "Tab", "return": "Enter",
	"backspace": "Backspace", "delete": "Delete", "insert": "Insert", "home": "Home", "end": "End",
	"pageup": "PgUp", "pagedown": "PgDn", "left": "←", "up": "↑", "right": "→", "down": "↓",
	"minus": "-", "equal": "=", "comma": ",", "period": ".", "slash": "/", "backslash": `\`,
	"semicolon": ";", "apostrophe": "'", "bracketleft": "[", "bracketright": "]",
	"pause": "Pause", "print": "PrtSc", "menu": "Menu",
}

// Caps are the keycap labels of h in the platform's own words (goos as
// runtime.GOOS): Super is Win on Windows and ⌘ on macOS.
func Caps(h contracts.Hotkey, goos string) []string {
	var out []string
	names := map[contracts.Modifier]string{
		contracts.ModCtrl: "Ctrl", contracts.ModAlt: "Alt", contracts.ModShift: "Shift", contracts.ModSuper: "Super",
	}
	switch goos {
	case "windows":
		names[contracts.ModSuper] = "Win"
	case "darwin":
		names[contracts.ModCtrl], names[contracts.ModAlt], names[contracts.ModShift], names[contracts.ModSuper] = "⌃", "⌥", "⇧", "⌘"
	}
	for _, m := range modOrder {
		if h.Mods&m.m != 0 {
			out = append(out, names[m.m])
		}
	}
	if h.Key == "" {
		return out
	}
	l, ok := keyLabels[h.Key]
	if !ok {
		l = strings.ToUpper(h.Key)
	}
	return append(out, l)
}

// Label joins Caps with "+", e.g. "Super+Space", for running text.
func Label(h contracts.Hotkey, goos string) string {
	return strings.Join(Caps(h, goos), "+")
}
