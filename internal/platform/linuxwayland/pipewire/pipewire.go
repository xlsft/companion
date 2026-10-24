// Package pipewire consumes one frame from a PipeWire video stream, as
// handed out by the xdg ScreenCast portal. The real implementation needs
// cgo and libpipewire-0.3; without cgo GrabFrame reports Unsupported.
package pipewire

import "image"

// Frame is one captured video frame.
type Frame struct {
	Image       *image.RGBA
	Format      string // negotiated SPA video format, e.g. "BGRx"
	DataType    string // MemPtr, MemFd or DmaBuf
	Modifier    uint64
	HasModifier bool
}

// Options tune stream negotiation.
type Options struct {
	// AllowDMABuf also offers LINEAR DMA-BUF formats; producers that only
	// allocate GPU buffers (niri, KWin) need it.
	AllowDMABuf bool
	// ImplicitModifier also offers DRM_FORMAT_MOD_INVALID, for producers
	// that list no LINEAR modifier. The mapped bytes are only meaningful if
	// the driver chose a linear layout.
	ImplicitModifier bool
}
