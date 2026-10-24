package contracts

import "context"

// DisplayID is stable for a physical output while it stays connected.
type DisplayID string

// Display describes one output of the virtual desktop.
type Display struct {
	ID   DisplayID
	Name string
	// Bounds in desktop (logical) coordinates.
	Bounds Rect
	// Native is the size of the output in physical pixels.
	Native PixelSize
	// Scale is native pixels per logical pixel (1, 1.25, 1.5, 2, ...).
	Scale float64
	// Rotation in degrees clockwise: 0, 90, 180 or 270.
	Rotation int
	Primary  bool
}

// Topology is a snapshot of all displays. Version increases on every
// hotplug, DPI or layout change; captures remember the version they were
// taken at and become stale when it changes (FR-SCREEN-005).
type Topology struct {
	Version  uint64
	Displays []Display
}

// Display returns the display with the given ID.
func (t Topology) Display(id DisplayID) (Display, bool) {
	for _, d := range t.Displays {
		if d.ID == id {
			return d, true
		}
	}
	return Display{}, false
}

// DisplayService reports the current topology and its changes.
type DisplayService interface {
	Topology(ctx context.Context) (Topology, error)
	// Watch delivers a new Topology after each change until ctx is done.
	Watch(ctx context.Context) (<-chan Topology, error)
}
