// Package buildinfo exposes version information injected at link time.
package buildinfo

import "runtime"

// Set with -ldflags "-X github.com/xlsft/companion/internal/buildinfo.Version=...".
var (
	Version = "0.0.1"
	Commit  = "unknown"
)

func String() string {
	return "companion " + Version + " (" + Commit + ") " + runtime.GOOS + "/" + runtime.GOARCH
}
