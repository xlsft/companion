package contracts

import (
	"context"
	"image"
)

// OverlaySurface is one transparent, always-on-top, non-focusable window
// covering a display. It is passive (click-through) unless an interactive
// region is set (FR-OVERLAY-001, FR-OVERLAY-008).
type OverlaySurface interface {
	Display() DisplayID
	// Present shows a frame rendered at the display's native resolution.
	// A fully transparent frame must leave no input-blocking area behind.
	Present(ctx context.Context, frame *image.RGBA) error
	// SetInteractive makes only region (display-local logical coordinates)
	// receive input; an empty Rect restores full click-through.
	SetInteractive(region Rect) error
	Close() error
}

// OverlayDamagePresenter is optionally implemented by surfaces that can
// update part of the screen. damage bounds every pixel that differs from
// the previously presented frame; outside it frame equals that frame. Used
// for animation, where uploading a whole display per frame would not keep
// up.
type OverlayDamagePresenter interface {
	PresentDamage(ctx context.Context, frame *image.RGBA, damage image.Rectangle) error
}

// OverlayHost creates overlay surfaces. Backends that cannot make a
// click-through surface return ErrUnsupported and the caller uses the
// fallback panel with an annotated capture (FR-OVERLAY-009).
type OverlayHost interface {
	Open(ctx context.Context, display DisplayID) (OverlaySurface, error)
}
