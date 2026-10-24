//go:build !linux

package main

import (
	"fmt"
	"os"
)

// The OC-5 spike covers Linux only; Windows and macOS overlays are OC-3/OC-4.
func main() {
	fmt.Fprintln(os.Stderr, "overlay spike: Linux only")
	os.Exit(2)
}
