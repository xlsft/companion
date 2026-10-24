// Package buildinfo exposes version information injected at link time.
package buildinfo

import "runtime"

// Set with -ldflags "-X github.com/xlsft/openclick/internal/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = "unknown"
)

func String() string {
	return "openclick " + Version + " (" + Commit + ") " + runtime.GOOS + "/" + runtime.GOARCH
}
