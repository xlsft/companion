// Package insert types dictated text into the focused field of any app
// (OC-78), through the first backend the desktop offers:
//
//	Wayland  RemoteDesktop portal (consent once, restore token kept)
//	         -> compositor virtual keyboard (niri, Sway, Hyprland: no portal keyboard)
//	X11      XTest, spare keycodes remapped to the characters typed
//
// With the clipboard fallback on, text a backend cannot reproduce exactly
// is pasted instead, and the clipboard is put back afterwards. Nothing
// here logs or keeps the text.
package insert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Typer is one way of sending keys to the focused window.
type Typer interface {
	Name() string
	// Ready connects and gets consent. An error means nothing was typed,
	// so the next backend may be tried.
	Ready(ctx context.Context) error
	Type(ctx context.Context, text string) error
	// Chord presses and releases a key chord: the paste shortcut.
	Chord(ctx context.Context, h contracts.Hotkey) error
	// Faithful reports whether Type reproduces text exactly; a backend
	// bound to the active keyboard layout drops what the layout lacks.
	Faithful(text string) bool
	Close() error
}

// Clip is saved clipboard content: one MIME type of it, or Empty.
type Clip struct {
	Mime  string
	Data  []byte
	Empty bool
}

// Clipboard reads and writes the system clipboard. Errors wrapping
// ErrUnsupported mean there is no way to (no tool, no selection owner
// protocol), so typing goes ahead instead.
type Clipboard interface {
	Save(ctx context.Context) (Clip, error)
	SetText(ctx context.Context, text string) error
	Restore(ctx context.Context, c Clip) error
}

// PasteChord is Ctrl+V: the paste shortcut of text fields almost
// everywhere (terminals that want Ctrl+Shift+V are the known exception).
var PasteChord = contracts.Hotkey{Mods: contracts.ModCtrl, Key: "v"}

// Inserter implements contracts.TextInserter over Typers in order of
// preference.
type Inserter struct {
	Typers    []Typer
	Clipboard Clipboard // nil: no clipboard fallback on this system
	// UseClipboard is the user's opt-in, read per insertion.
	UseClipboard func() bool
	// Settle is how long the target gets to read the clipboard before it
	// is restored; 0 means 400ms.
	Settle time.Duration

	mu sync.Mutex // one insertion at a time: keys must not interleave
}

var _ contracts.TextInserter = (*Inserter)(nil)

func (in *Inserter) InsertText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	var denied bool
	var last error
	for _, t := range in.Typers {
		err := t.Ready(ctx)
		if err == nil {
			return in.insert(ctx, t, text)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case errors.Is(err, contracts.ErrPermissionDenied):
			denied = true
		case !errors.Is(err, contracts.ErrUnsupported):
			last = fmt.Errorf("%s: %w", t.Name(), err)
		}
	}
	switch {
	case denied:
		return contracts.PermissionDenied(contracts.CapTextInsertion)
	case last != nil:
		return &contracts.CapabilityError{Capability: contracts.CapTextInsertion, Err: last}
	}
	return contracts.Unsupported(contracts.CapTextInsertion)
}

func (in *Inserter) insert(ctx context.Context, t Typer, text string) error {
	if !t.Faithful(text) && in.Clipboard != nil && in.UseClipboard != nil && in.UseClipboard() {
		err := in.paste(ctx, t, text)
		if !errors.Is(err, contracts.ErrUnsupported) {
			return err
		}
	}
	return t.Type(ctx, text)
}

// paste puts text on the clipboard, presses the paste chord and restores
// what was there, even when ctx ends in between.
func (in *Inserter) paste(ctx context.Context, t Typer, text string) error {
	saved, err := in.Clipboard.Save(ctx)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := in.Clipboard.SetText(ctx, text); err != nil {
		_ = in.Clipboard.Restore(rctx, saved)
		return err
	}
	err = t.Chord(ctx, PasteChord)
	settle := in.Settle
	if settle == 0 {
		settle = 400 * time.Millisecond
	}
	// The app reads the clipboard after the chord arrives; restoring at
	// once could make it paste the old content.
	select {
	case <-time.After(settle):
	case <-ctx.Done():
	}
	if rerr := in.Clipboard.Restore(rctx, saved); err == nil {
		err = rerr
	}
	return err
}

// Close releases every backend (portal sessions, connections).
func (in *Inserter) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	var errs []error
	for _, t := range in.Typers {
		errs = append(errs, t.Close())
	}
	return errors.Join(errs...)
}

// X keysyms used outside the Unicode range.
const (
	symReturn   = 0xff0d
	symTab      = 0xff09
	symControlL = 0xffe3
)

// Keysym is the X keysym that types r, 0 for control characters, which are
// skipped. Latin-1 keysyms equal their code points; the rest of Unicode is
// 0x01000000 + code point.
func Keysym(r rune) uint32 {
	switch {
	case r == '\n':
		return symReturn
	case r == '\t':
		return symTab
	case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0, r > 0x10ffff:
		return 0
	case r < 0x100:
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}

// Keysyms maps text to keysyms, dropping what has none.
func Keysyms(text string) []uint32 {
	out := make([]uint32, 0, len(text))
	for _, r := range text {
		if s := Keysym(r); s != 0 {
			out = append(out, s)
		}
	}
	return out
}

// asciiOnly is the Faithful test of backends tied to the keyboard layout:
// printable ASCII, newline and tab are on every Latin layout.
func asciiOnly(text string) bool {
	for _, r := range text {
		if (r < 0x20 && r != '\n' && r != '\t') || r > 0x7e {
			return false
		}
	}
	return true
}

// keysymName is sym as an xkb keymap writes it.
func keysymName(sym uint32) string {
	switch sym {
	case symReturn:
		return "Return"
	case symTab:
		return "Tab"
	case symControlL:
		return "Control_L"
	}
	if sym < 0x100 {
		return fmt.Sprintf("U%04X", sym)
	}
	if sym&0xff000000 == 0x01000000 {
		return fmt.Sprintf("U%04X", sym&0x00ffffff)
	}
	return fmt.Sprintf("0x%08x", sym)
}

// keymap is an xkb keymap with one key per keysym: syms[i] is on xkb
// keycode 9+i, i.e. evdev code 1+i, which the virtual keyboard sends. A
// Control_L key is also the Control modifier, for the paste chord.
func keymap(syms []uint32) string {
	var b strings.Builder
	fmt.Fprintf(&b, "xkb_keymap {\nxkb_keycodes \"companion\" {\nminimum = 8;\nmaximum = %d;\n", 9+len(syms))
	for i := range syms {
		fmt.Fprintf(&b, "<K%d> = %d;\n", i+1, 9+i)
	}
	b.WriteString("};\nxkb_types \"companion\" { include \"complete\" };\nxkb_compat \"companion\" { include \"complete\" };\nxkb_symbols \"companion\" {\n")
	for i, s := range syms {
		fmt.Fprintf(&b, "key <K%d> {[ %s ]};\n", i+1, keysymName(s))
		if s == symControlL {
			fmt.Fprintf(&b, "modifier_map Control { <K%d> };\n", i+1)
		}
	}
	b.WriteString("};\n};\n")
	return b.String()
}

// chunks splits syms into consecutive runs of at most max distinct
// keysyms each, so a run fits the keycodes a backend can remap at once.
func chunks(syms []uint32, max int) [][]uint32 {
	var out [][]uint32
	start := 0
	seen := map[uint32]bool{}
	for i, s := range syms {
		if !seen[s] && len(seen) == max {
			out = append(out, syms[start:i])
			start = i
			clear(seen)
		}
		seen[s] = true
	}
	if start < len(syms) {
		out = append(out, syms[start:])
	}
	return out
}

// distinct lists the keysyms of syms once each, in first-seen order.
func distinct(syms []uint32) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, s := range syms {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
