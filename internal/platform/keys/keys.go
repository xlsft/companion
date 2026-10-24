// Package keys maps contracts.Hotkey key names to X keysyms and to the
// xkbcommon keysym names that the GlobalShortcuts portal expects, and parses
// chords in both directions. X11 and Wayland share keysyms, so one table
// serves both Linux backends.
package keys

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xlsft/companion/pkg/contracts"
)

type key struct {
	sym uint32
	xkb string // xkbcommon keysym name
}

var table = func() map[string]key {
	m := map[string]key{
		"space":        {0x0020, "space"},
		"escape":       {0xff1b, "Escape"},
		"grave":        {0x0060, "grave"},
		"tab":          {0xff09, "Tab"},
		"return":       {0xff0d, "Return"},
		"backspace":    {0xff08, "BackSpace"},
		"delete":       {0xffff, "Delete"},
		"insert":       {0xff63, "Insert"},
		"home":         {0xff50, "Home"},
		"end":          {0xff57, "End"},
		"pageup":       {0xff55, "Prior"},
		"pagedown":     {0xff56, "Next"},
		"left":         {0xff51, "Left"},
		"up":           {0xff52, "Up"},
		"right":        {0xff53, "Right"},
		"down":         {0xff54, "Down"},
		"minus":        {0x002d, "minus"},
		"equal":        {0x003d, "equal"},
		"comma":        {0x002c, "comma"},
		"period":       {0x002e, "period"},
		"slash":        {0x002f, "slash"},
		"backslash":    {0x005c, "backslash"},
		"semicolon":    {0x003b, "semicolon"},
		"apostrophe":   {0x0027, "apostrophe"},
		"bracketleft":  {0x005b, "bracketleft"},
		"bracketright": {0x005d, "bracketright"},
		"pause":        {0xff13, "Pause"},
		"print":        {0xff61, "Print"},
		"menu":         {0xff67, "Menu"},
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = key{uint32(c), string(c)}
	}
	for c := '0'; c <= '9'; c++ {
		m[string(c)] = key{uint32(c), string(c)}
	}
	for i := 1; i <= 24; i++ {
		n := strconv.Itoa(i)
		m["f"+n] = key{0xffbe + uint32(i-1), "F" + n}
	}
	return m
}()

// Other spellings accepted by Parse and in portal trigger descriptions.
var aliases = map[string]string{
	"esc": "escape", "enter": "return", "del": "delete", "ins": "insert",
	"prior": "pageup", "next": "pagedown", "page_up": "pageup", "page_down": "pagedown",
	"pgup": "pageup", "pgdown": "pagedown", "`": "grave", "-": "minus", "=": "equal",
	",": "comma", ".": "period", "/": "slash", "\\": "backslash", ";": "semicolon",
	"'": "apostrophe", "[": "bracketleft", "]": "bracketright",
}

func canonical(name string) (string, bool) {
	n := strings.ToLower(name)
	if a, ok := aliases[n]; ok {
		n = a
	}
	_, ok := table[n]
	return n, ok
}

// Keysym returns the X keysym of a contracts.Hotkey key name. Letters map
// to the lower-case keysym, which is what the keyboard mapping lists in its
// first column.
func Keysym(name string) (uint32, bool) {
	k, ok := table[name]
	return k.sym, ok
}

// XKBName returns the xkbcommon keysym name, e.g. "space" or "Escape".
func XKBName(name string) (string, bool) {
	k, ok := table[name]
	return k.xkb, ok
}

var modOrder = []struct {
	m      contracts.Modifier
	name   string // our chord syntax
	portal string // XDG shortcuts syntax
}{
	{contracts.ModCtrl, "ctrl", "CTRL"},
	{contracts.ModAlt, "alt", "ALT"},
	{contracts.ModShift, "shift", "SHIFT"},
	{contracts.ModSuper, "super", "LOGO"},
}

var modNames = map[string]contracts.Modifier{
	"ctrl": contracts.ModCtrl, "control": contracts.ModCtrl, "primary": contracts.ModCtrl,
	"alt": contracts.ModAlt, "mod1": contracts.ModAlt,
	"shift": contracts.ModShift,
	"super": contracts.ModSuper, "logo": contracts.ModSuper, "meta": contracts.ModSuper,
	"win": contracts.ModSuper, "mod4": contracts.ModSuper, "mod": contracts.ModSuper,
}

// String formats h as "ctrl+alt+space".
func String(h contracts.Hotkey) string {
	var b strings.Builder
	for _, m := range modOrder {
		if h.Mods&m.m != 0 {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(h.Key)
	return b.String()
}

// Parse reads a chord such as "ctrl+alt+space" or "Ctrl+Alt+Space". It
// accepts the portal's own syntax ("CTRL+ALT+space") and the
// human-readable trigger descriptions portals return, so the granted chord
// can be shown back in our terms.
func Parse(s string) (contracts.Hotkey, error) {
	var h contracts.Hotkey
	parts := strings.Split(strings.TrimSpace(s), "+")
	// "ctrl++" means ctrl and the plus key, which we do not support; an
	// empty part is always an error.
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return contracts.Hotkey{}, fmt.Errorf("keys: empty part in chord %q", s)
		}
		if i < len(parts)-1 {
			m, ok := modNames[strings.ToLower(p)]
			if !ok {
				return contracts.Hotkey{}, fmt.Errorf("keys: unknown modifier %q", p)
			}
			h.Mods |= m
			continue
		}
		k, ok := canonical(p)
		if !ok {
			return contracts.Hotkey{}, fmt.Errorf("keys: unknown key %q", p)
		}
		h.Key = k
	}
	return h, nil
}

// PortalTrigger formats h in the XDG shortcuts syntax used for the
// GlobalShortcuts preferred_trigger, e.g. "CTRL+ALT+space".
func PortalTrigger(h contracts.Hotkey) (string, error) {
	x, ok := XKBName(h.Key)
	if !ok {
		return "", fmt.Errorf("keys: unknown key %q", h.Key)
	}
	var b strings.Builder
	for _, m := range modOrder {
		if h.Mods&m.m != 0 {
			b.WriteString(m.portal + "+")
		}
	}
	b.WriteString(x)
	return b.String(), nil
}
