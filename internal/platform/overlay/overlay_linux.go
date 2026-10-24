//go:build linux

// Package overlay picks the click-through overlay backend for the session
// (OC-5): wlr-layer-shell on Wayland, an override-redirect ARGB window on
// X11. Where neither works the caller falls back to the panel with an
// annotated capture (FR-OVERLAY-009).
package overlay

import (
	"context"
	"fmt"

	wloverlay "github.com/xlsft/companion/internal/platform/linuxwayland/overlay"
	x11overlay "github.com/xlsft/companion/internal/platform/linuxx11/overlay"
	"github.com/xlsft/companion/pkg/contracts"
)

// Host is an overlay host the caller must Close.
type Host interface {
	contracts.OverlayHost
	Close() error
}

// New opens the overlay host for the session in report.
func New(ctx context.Context, report contracts.CapabilityReport) (Host, error) {
	if st, ok := report.Capabilities[contracts.CapOverlayClickThrough]; ok && st.Status == contracts.StatusUnavailable {
		return nil, contracts.Unsupported(contracts.CapOverlayClickThrough)
	}
	switch report.Session {
	case contracts.SessionWayland:
		h, err := wloverlay.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("wayland overlay: %w", err)
		}
		return h, nil
	case contracts.SessionX11:
		h, err := x11overlay.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("x11 overlay: %w", err)
		}
		return h, nil
	}
	return nil, contracts.Unsupported(contracts.CapOverlayClickThrough)
}
