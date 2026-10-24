//go:build unix

package desktop

import (
	"os"
	"syscall"
)

// Reexec replaces the process image with exe, keeping the pid, arguments
// and environment; it returns only on failure.
func Reexec(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
