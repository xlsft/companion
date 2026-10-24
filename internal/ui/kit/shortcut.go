package kit

import (
	"image"
	"runtime"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// ShortcutRecorder is a field that records a key chord: click it (or
// focus it and press Enter or Space), press the chord, and it shows as
// keycaps. Escape is recorded like any key, so recording ends when focus
// leaves the field rather than on Esc. The value is in internal/shortcut
// syntax. UI goroutine only.
type ShortcutRecorder struct {
	click     widget.Clickable
	recording bool
	held      contracts.Modifier // modifiers down while recording
	value     string
	changed   bool
	focusNext bool
}

// Value is the recorded chord, "" when none.
func (r *ShortcutRecorder) Value() string { return r.value }

// Set replaces the value, e.g. with the stored chord; it is ignored while
// recording so a refresh does not undo the user's keys.
func (r *ShortcutRecorder) Set(chord string) {
	if !r.recording {
		r.value = chord
	}
}

// Recording reports whether the field waits for a chord.
func (r *ShortcutRecorder) Recording() bool { return r.recording }

// Record starts recording, as a click would.
func (r *ShortcutRecorder) Record() {
	r.recording, r.held, r.focusNext = true, 0, true
}

// Press feeds a key press as the window would, for tests.
func (r *ShortcutRecorder) Press(name key.Name, mods key.Modifiers) {
	r.press(key.Event{Name: name, Modifiers: mods, State: key.Press})
}

// Update handles the frame's input and reports whether a new chord was
// recorded.
func (r *ShortcutRecorder) Update(gtx layout.Context) bool {
	if r.click.Clicked(gtx) && gtx.Enabled() {
		if r.recording {
			r.recording = false
		} else {
			r.Record()
		}
	}
	if r.focusNext {
		r.focusNext = false
		gtx.Execute(key.FocusCmd{Tag: r})
	}
	all := key.ModCtrl | key.ModShift | key.ModAlt | key.ModSuper | key.ModCommand
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: r},
			key.Filter{Focus: r, Name: "", Optional: all},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			if !e.Focus {
				r.recording = false
			}
		case key.Event:
			if r.recording {
				r.press(e)
			}
		}
	}
	ch := r.changed
	r.changed = false
	return ch
}

func (r *ShortcutRecorder) press(e key.Event) {
	mods := modsOf(e.Modifiers)
	if m, isMod := modifierKey(e.Name); isMod {
		if e.State == key.Press {
			r.held = mods | m
		} else {
			r.held = mods &^ m
		}
		return
	}
	if e.State != key.Press {
		return
	}
	k, ok := keyName(e.Name)
	if !ok {
		return
	}
	r.value = shortcut.Format(contracts.Hotkey{Mods: mods, Key: k})
	r.recording, r.held, r.changed = false, 0, true
}

func modsOf(m key.Modifiers) contracts.Modifier {
	var out contracts.Modifier
	if m.Contain(key.ModCtrl) {
		out |= contracts.ModCtrl
	}
	if m.Contain(key.ModAlt) {
		out |= contracts.ModAlt
	}
	if m.Contain(key.ModShift) {
		out |= contracts.ModShift
	}
	// Cmd on macOS is the Super of our chords.
	if m.Contain(key.ModSuper) || m.Contain(key.ModCommand) {
		out |= contracts.ModSuper
	}
	return out
}

func modifierKey(n key.Name) (contracts.Modifier, bool) {
	switch n {
	case key.NameCtrl:
		return contracts.ModCtrl, true
	case key.NameAlt:
		return contracts.ModAlt, true
	case key.NameShift:
		return contracts.ModShift, true
	case key.NameSuper, key.NameCommand:
		return contracts.ModSuper, true
	}
	return 0, false
}

var gioKeys = map[key.Name]string{
	key.NameSpace: "space", key.NameEscape: "escape", key.NameTab: "tab",
	key.NameReturn: "return", key.NameEnter: "return",
	key.NameDeleteBackward: "backspace", key.NameDeleteForward: "delete",
	key.NameHome: "home", key.NameEnd: "end", key.NamePageUp: "pageup", key.NamePageDown: "pagedown",
	key.NameLeftArrow: "left", key.NameUpArrow: "up", key.NameRightArrow: "right", key.NameDownArrow: "down",
	"`": "grave", "-": "minus", "=": "equal", ",": "comma", ".": "period", "/": "slash", `\`: "backslash",
	";": "semicolon", "'": "apostrophe", "[": "bracketleft", "]": "bracketright",
}

// keyName maps a Gio key name to a chord key name.
func keyName(n key.Name) (string, bool) {
	if k, ok := gioKeys[n]; ok {
		return k, true
	}
	s := strings.ToLower(string(n))
	if h, err := shortcut.Parse(s); err == nil && h.Mods == 0 {
		return h.Key, true
	}
	return "", false
}

// RecorderStyle is what a ShortcutRecorder shows around the keys.
type RecorderStyle struct {
	// Placeholder is shown without a value, Prompt while recording.
	Placeholder, Prompt string
	// Desc is the accessible name, e.g. "Talk shortcut".
	Desc string
	// Invalid outlines the field in the error colour.
	Invalid bool
}

// Layout draws the field: the chord as keycaps, or the prompt with the
// modifiers held so far.
func (r *ShortcutRecorder) Layout(gtx layout.Context, th *theme.Theme, s RecorderStyle) layout.Dimensions {
	if !gtx.Enabled() {
		r.recording = false
	}
	focused := gtx.Focused(r) || gtx.Focused(&r.click)
	border := th.Pal.Divider
	switch {
	case r.recording:
		border = th.Pal.AccentLine
	case s.Invalid:
		border = th.Pal.Error
	}
	bg := th.Pal.Raised
	if r.recording {
		bg = th.Pal.AccentTint
	} else if r.click.Hovered() && gtx.Enabled() {
		bg = th.Pal.Hover
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return FocusRing(gtx, th, focused && !r.recording, func(gtx layout.Context) layout.Dimensions {
		return r.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(s.Desc).Add(gtx.Ops)
			dims := Box(gtx, th, bg, border, 4, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(8), Right: th.Dp(10)}.Layout(gtx, FixedH(th, 36, func(gtx layout.Context) layout.Dimensions {
					return r.content(gtx, th, s)
				}))
			})
			// The key focus target covers the whole field.
			defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, r)
			return dims
		})
	})
}

func (r *ShortcutRecorder) content(gtx layout.Context, th *theme.Theme, s RecorderStyle) layout.Dimensions {
	var caps []string
	var tail string
	switch {
	case r.recording:
		caps = shortcut.Caps(contracts.Hotkey{Mods: r.held}, runtime.GOOS)
		tail = s.Prompt
	case r.value != "":
		if h, err := shortcut.Parse(r.value); err == nil {
			caps = shortcut.Caps(h, runtime.GOOS)
		} else {
			tail = r.value
		}
	default:
		tail = s.Placeholder
	}
	ws := make([]layout.Widget, 0, len(caps)+1)
	for _, c := range caps {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return Kbd(gtx, th, c) })
	}
	if tail != "" {
		col := th.Pal.Subtle
		if r.recording {
			col = th.Pal.AccentText
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			l := label(th, 13, tail, col)
			l.MaxLines = 1
			return l.Layout(gtx)
		})
	}
	minX := gtx.Constraints.Min.X
	gtx.Constraints.Min = image.Point{}
	dims := HStack(th, 4, ws...)(gtx)
	dims.Size.X = max(dims.Size.X, minX) // the field spans its column
	return dims
}

// Caps are the labels the field shows for its value, for callers that
// record what is on screen.
func (r *ShortcutRecorder) Caps() []string {
	h, err := shortcut.Parse(r.value)
	if err != nil {
		return nil
	}
	return shortcut.Caps(h, runtime.GOOS)
}
