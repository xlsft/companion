package contracts

import "context"

// HotkeyID is chosen by the caller, e.g. "talk", "dictate", "cancel".
type HotkeyID string

// Modifier is a bit set of modifier keys.
type Modifier uint8

const (
	ModShift Modifier = 1 << iota
	ModCtrl
	ModAlt
	ModSuper // Win / Cmd
)

// Hotkey is a key chord. Key uses lower-case names: "a".."z", "0".."9",
// "space", "escape", "f1".."f24", "grave", ...
type Hotkey struct {
	Mods Modifier
	Key  string
}

type HotkeyBinding struct {
	ID     HotkeyID
	Hotkey Hotkey
	// Description is shown by portal-based backends in the system UI.
	Description string
}

type HotkeyEventKind int

const (
	HotkeyPressed HotkeyEventKind = iota
	HotkeyReleased
)

type HotkeyEvent struct {
	ID   HotkeyID
	Kind HotkeyEventKind
}

// HotkeyService registers global shortcuts. Backends without key-up events
// (some Wayland portals) report SupportsRelease()==false and the session
// layer switches hold-to-talk to toggle mode (PRD §4.2).
type HotkeyService interface {
	// Register replaces the full set of bindings. It may show a system
	// consent dialog on portal backends; the returned bindings reflect what
	// the system actually granted (the user may change the chord there).
	Register(ctx context.Context, bindings []HotkeyBinding) ([]HotkeyBinding, error)
	Unregister(ctx context.Context) error
	SupportsRelease() bool
	// Events is closed when the service is stopped.
	Events() <-chan HotkeyEvent
}
