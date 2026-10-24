//go:build !linux

// Package overlay picks the click-through overlay backend for the session.
// Windows and macOS overlays are OC-3/OC-4; until then the caller uses the
// fallback panel.
package overlay

import (
	"context"

	"github.com/xlsft/companion/pkg/contracts"
)

// Host is an overlay host the caller must Close.
type Host interface {
	contracts.OverlayHost
	Close() error
}

// New reports the overlay as unsupported on this OS.
func New(context.Context, contracts.CapabilityReport) (Host, error) {
	return nil, contracts.Unsupported(contracts.CapOverlayClickThrough)
}
