package control

import (
	"io/fs"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

const peerCredSupported = true

// peerUID reads the connecting process's uid with SO_PEERCRED; the kernel
// fills it at connect time, so the client cannot forge it.
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return cred.Uid, nil
}

func fileOwner(st fs.FileInfo) (uint32, bool) {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return sys.Uid, true
	}
	return 0, false
}
