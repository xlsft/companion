//go:build !linux && !darwin && !windows

package hw

import "errors"

// FreeDiskSpace is not implemented on this OS.
func FreeDiskSpace(string) (uint64, error) { return 0, errors.New("free disk space: unsupported OS") }
