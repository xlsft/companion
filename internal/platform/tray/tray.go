// Package tray implements contracts.Tray. Linux (StatusNotifierItem over
// D-Bus) and Windows (Shell_NotifyIcon) are pure Go via fyne.io/systray;
// macOS needs cgo, so a CGO_ENABLED=0 darwin build gets a stub that reports
// contracts.Unsupported(contracts.CapTray).
package tray

import "github.com/xlsft/openclick/pkg/contracts"

// New returns the tray for the current platform. Only one tray may exist per
// process: the underlying OS icon is a singleton.
func New() contracts.Tray { return newTray() }
