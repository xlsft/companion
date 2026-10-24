//go:build !linux

package control

import (
	"io/fs"
	"net"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Peer credentials are only implemented for Linux, where the control
// socket is the hotkey fallback; Listen refuses elsewhere rather than
// accepting unauthenticated peers.
const peerCredSupported = false

func peerUID(*net.UnixConn) (uint32, error) {
	return 0, contracts.Unsupported(contracts.CapGlobalHotkey)
}

func fileOwner(fs.FileInfo) (uint32, bool) { return 0, false }
