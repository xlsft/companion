//go:build windows || (darwin && cgo)

package tray

// Windows always has a notification area and macOS a menu bar.
func probe() error { return nil }

func presetAppearance(func()) {}
