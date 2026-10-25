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
	// TriggerDescription is the system's human-readable form of the chord
	// it actually granted (portal backends), e.g. "Ctrl+Alt+Space". Empty
	// when the backend binds Hotkey exactly as requested.
	TriggerDescription string
}

type HotkeyEventKind int

const (
	HotkeyPressed HotkeyEventKind = iota
	HotkeyReleased
	// HotkeyToggled is a press from a source that never sends the matching
	// release, e.g. a compositor keybinding running "companion ctl
	// talk-toggle". It means "start or stop", whatever the voice mode.
	HotkeyToggled
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

// DoubleTapper is a HotkeyService that can also toggle a binding on a
// double tap of one modifier key (OC-133). A backend that cannot see a
// lone modifier (GlobalShortcuts portal, compositors without release
// binds) returns Unsupported(CapDoubleTap).
type DoubleTapper interface {
	// SetDoubleTap makes a double tap of key ("ctrl", "shift", "alt" or
	// "super", either side) send HotkeyToggled for id; "" turns it off.
	SetDoubleTap(ctx context.Context, id HotkeyID, key string) error
}
