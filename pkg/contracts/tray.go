package contracts

import "context"

// TrayMenuItem is one entry of the tray / menu bar menu. ID is chosen by the
// caller and passed back on selection; Label is already localised.
type TrayMenuItem struct {
	ID       string
	Label    string
	Disabled bool
}

// Tray is the system tray (Windows, Linux StatusNotifierItem) or menu bar
// (macOS) icon that keeps the app reachable without a main window
// (FR-UI-002).
type Tray interface {
	// Start shows the icon with items. onSelect receives the ID of a chosen
	// item on a tray goroutine. Returns Unsupported(CapTray) when the
	// desktop has no tray host; callers must then keep a window open.
	Start(ctx context.Context, tooltip string, items []TrayMenuItem, onSelect func(id string)) error
	// SetItems replaces the whole menu, e.g. when the app leaves setup mode.
	SetItems(items []TrayMenuItem) error
	// Stop removes the icon. A stopped tray cannot be started again.
	Stop() error
}
