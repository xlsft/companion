package tray

import (
	"fmt"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/openclick/pkg/contracts"
)

const watcherName = "org.kde.StatusNotifierWatcher"

// probe checks for a StatusNotifierWatcher: without one (plain GNOME, bare
// compositors without a bar) the icon is exported but never shown, and
// closing the last window would strand the app.
func probe() error {
	unsupported := func(format string, a ...any) error {
		return &contracts.CapabilityError{
			Capability: contracts.CapTray,
			Err:        fmt.Errorf("%w: "+format, append([]any{contracts.ErrUnsupported}, a...)...),
		}
	}
	// A private connection: systray closes the shared session bus on exit.
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return unsupported("session bus: %v", err)
	}
	defer conn.Close()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&has); err != nil {
		return unsupported("NameHasOwner: %v", err)
	}
	if !has {
		return unsupported("no %s on session bus", watcherName)
	}
	return nil
}

// presetAppearance stores icon and tooltip before the D-Bus properties are
// exported: on Linux systray's ready callback races the export.
func presetAppearance(apply func()) { apply() }
