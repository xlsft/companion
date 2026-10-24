//go:build !linux

package main

import (
	"fmt"
	"os"
)

// xdg-desktop-portal exists only on Linux (and BSDs, not targeted).
func main() {
	fmt.Fprintln(os.Stderr, "portals spike: Linux only")
	os.Exit(2)
}
