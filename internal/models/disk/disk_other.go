//go:build !linux && !darwin && !windows

package disk

import "errors"

// Free is not implemented on this OS.
func Free(string) (uint64, error) { return 0, errors.New("free disk space: unsupported OS") }
