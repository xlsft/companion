//go:build windows

package disk

import "golang.org/x/sys/windows"

// Free returns bytes available to the calling user (quota aware).
func Free(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
