package desktop

import (
	"os"
	"os/exec"
)

// Reexec starts exe as a new process; Windows cannot replace a running
// image, so the caller exits once this returns nil.
func Reexec(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
