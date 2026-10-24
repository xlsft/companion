//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hotkeys spike: Linux only (OC-57)")
	os.Exit(1)
}
