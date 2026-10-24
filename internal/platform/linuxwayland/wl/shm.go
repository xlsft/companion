//go:build linux

package wl

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// ShmFile is an anonymous shared-memory file mapped into this process, used
// as the backing store of a wl_shm_pool.
type ShmFile struct {
	Fd   int
	Data []byte
}

func NewShmFile(size int) (*ShmFile, error) {
	fd, err := unix.MemfdCreate("openclick-wl-shm", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("wl: memfd_create: %w", err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("wl: ftruncate: %w", err)
	}
	// Sealing against shrinking lets the compositor map it without fearing
	// SIGBUS; failure is harmless.
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK)
	data, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("wl: mmap: %w", err)
	}
	return &ShmFile{Fd: fd, Data: data}, nil
}

func (f *ShmFile) Close() error {
	var err error
	if f.Data != nil {
		err = unix.Munmap(f.Data)
		f.Data = nil
	}
	if f.Fd >= 0 {
		if cerr := unix.Close(f.Fd); err == nil {
			err = cerr
		}
		f.Fd = -1
	}
	return err
}
