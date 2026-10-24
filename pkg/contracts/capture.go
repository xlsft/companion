package contracts

import (
	"context"
	"image"
	"time"
)

// CaptureID uniquely identifies one capture within the process lifetime.
type CaptureID string

// CaptureTarget selects what to capture (FR-SCREEN-003).
type CaptureTarget int

const (
	// CaptureActiveDisplay captures the display under the pointer or with
	// the focused window, as the backend can best determine.
	CaptureActiveDisplay CaptureTarget = iota
	CaptureAllDisplays
	CaptureDisplay
	CaptureRegion
)

type CaptureRequest struct {
	Target CaptureTarget
	// Display is required for CaptureDisplay.
	Display DisplayID
	// Region in desktop coordinates, required for CaptureRegion.
	Region Rect
}

// CaptureMetadata carries everything needed to map model coordinates back
// to the screen (PRD §9.7, OC-41).
type CaptureMetadata struct {
	ID        CaptureID
	DisplayID DisplayID
	// Native is the size of Image in physical pixels.
	Native PixelSize
	// Model is the size of the image actually sent to the model after
	// downscaling; zero until the image is prepared for a request.
	Model PixelSize
	// Bounds is the captured area in desktop (logical) coordinates.
	Bounds Rect
	// Scale is native pixels per logical pixel for this capture.
	Scale    float64
	Rotation int
	// DesktopOrigin is the top-left of the virtual desktop at capture time,
	// in desktop coordinates (usually 0,0; may differ with negative origins).
	DesktopOrigin   Point
	TopologyVersion uint64
	Timestamp       time.Time
	// PlatformIDs holds backend-specific identifiers such as a PipeWire node
	// id or a window handle, for diagnostics only.
	PlatformIDs map[string]string
}

// Capture is one captured display or region. Image lives only in memory by
// default (FR-SCREEN-006) and must be released with Release when done.
type Capture struct {
	Meta  CaptureMetadata
	Image *image.RGBA
}

// Release drops the pixel buffer so it can be garbage collected.
func (c *Capture) Release() { c.Image = nil }

// ScreenCapturer takes screenshots on explicit request (FR-SCREEN-001).
// CaptureAllDisplays returns one Capture per display.
type ScreenCapturer interface {
	Capture(ctx context.Context, req CaptureRequest) ([]*Capture, error)
}
