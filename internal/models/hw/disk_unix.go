//go:build linux || darwin

package hw

import "golang.org/x/sys/unix"

// FreeDiskSpace returns bytes available to an unprivileged user on the
// filesystem holding dir.
func FreeDiskSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	// Field widths differ per OS/arch, hence the conversions.
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
