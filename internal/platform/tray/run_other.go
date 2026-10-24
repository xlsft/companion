//go:build linux || windows

package tray

import (
	"runtime"

	"fyne.io/systray"
)

// On Windows the notify window and its message loop must share one OS
// thread, which systray.Run guarantees when it owns a locked goroutine.
func run(onReady func()) (stop func()) {
	go func() {
		runtime.LockOSThread()
		systray.Run(onReady, nil)
	}()
	return systray.Quit
}

func platformIcon() []byte {
	if runtime.GOOS == "windows" {
		return iconICO(iconPNG())
	}
	return iconPNG()
}
