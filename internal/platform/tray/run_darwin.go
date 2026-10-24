//go:build darwin && cgo

package tray

import "fyne.io/systray"

// Gio owns the NSApplication run loop on the main thread, so the status item
// attaches to it instead of running a second loop. Not verified on macOS yet.
func run(onReady func()) (stop func()) {
	start, end := systray.RunWithExternalLoop(onReady, nil)
	start()
	return end
}

func platformIcon() []byte { return iconPNG() }
