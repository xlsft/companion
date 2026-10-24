// Package coordinates maps model answers on a capture back to the screen
// (PRD §9.7):
//
//	normalized model coordinates
//	→ model image pixels
//	→ native capture pixels
//	→ display-local logical coordinates
//	→ virtual desktop coordinates
//	→ overlay or input backend
//
// The model image is a uniformly resized copy of the native capture, so a
// normalized point is the same in both; rotation is the only transform
// between capture pixels and the screen besides scaling.
package coordinates

import (
	"errors"
	"fmt"
	"math"

	"github.com/xlsft/companion/pkg/contracts"
)

var ErrInvalidMetadata = errors.New("invalid capture metadata")

// scaleTolerance absorbs rounding of fractional scales (e.g. 1.25 on an
// odd-sized region) when checking Native against Bounds×Scale.
const scaleTolerance = 1.5

// Validate checks that metadata is internally consistent (OC-41).
func Validate(m contracts.CaptureMetadata) error {
	switch {
	case m.ID == "":
		return fmt.Errorf("%w: missing capture id", ErrInvalidMetadata)
	case m.Native.W <= 0 || m.Native.H <= 0:
		return fmt.Errorf("%w: native size %dx%d", ErrInvalidMetadata, m.Native.W, m.Native.H)
	case m.Bounds.Empty():
		return fmt.Errorf("%w: empty bounds", ErrInvalidMetadata)
	case m.Scale <= 0:
		return fmt.Errorf("%w: scale %g", ErrInvalidMetadata, m.Scale)
	case m.Model.W < 0 || m.Model.H < 0:
		return fmt.Errorf("%w: model size %dx%d", ErrInvalidMetadata, m.Model.W, m.Model.H)
	}
	if m.Rotation%90 != 0 {
		return fmt.Errorf("%w: rotation %d", ErrInvalidMetadata, m.Rotation)
	}
	// Native pixels are in buffer orientation; Bounds are on-screen.
	nw, nh := float64(m.Native.W), float64(m.Native.H)
	if quarterTurns(m.Rotation)%2 == 1 {
		nw, nh = nh, nw
	}
	if math.Abs(nw-m.Bounds.Width()*m.Scale) > scaleTolerance ||
		math.Abs(nh-m.Bounds.Height()*m.Scale) > scaleTolerance {
		return fmt.Errorf("%w: native %gx%g does not match bounds %gx%g at scale %g",
			ErrInvalidMetadata, nw, nh, m.Bounds.Width(), m.Bounds.Height(), m.Scale)
	}
	return nil
}

func quarterTurns(rotation int) int { return ((rotation/90)%4 + 4) % 4 }

// ModelPixelToNormalized converts a point in model image pixels, for
// providers that answer in pixels instead of normalized coordinates.
func ModelPixelToNormalized(m contracts.CaptureMetadata, px contracts.Point) (contracts.Point, error) {
	if m.Model.W <= 0 || m.Model.H <= 0 {
		return contracts.Point{}, fmt.Errorf("%w: model size unknown", ErrInvalidMetadata)
	}
	return contracts.Point{X: px.X / float64(m.Model.W), Y: px.Y / float64(m.Model.H)}, nil
}

// NormalizedToDesktop maps a normalized capture point to desktop
// coordinates. Rotation is the clockwise rotation that turns the capture
// buffer into what is shown on screen.
func NormalizedToDesktop(m contracts.CaptureMetadata, p contracts.Point) (contracts.Point, error) {
	if err := Validate(m); err != nil {
		return contracts.Point{}, err
	}
	if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
		return contracts.Point{}, fmt.Errorf("point (%g,%g) outside normalized capture", p.X, p.Y)
	}
	u, v := p.X, p.Y
	switch quarterTurns(m.Rotation) {
	case 1:
		u, v = 1-v, u
	case 2:
		u, v = 1-u, 1-v
	case 3:
		u, v = v, 1-u
	}
	return contracts.Point{
		X: m.Bounds.Min.X + u*m.Bounds.Width(),
		Y: m.Bounds.Min.Y + v*m.Bounds.Height(),
	}, nil
}

// DesktopToNormalized is the inverse of NormalizedToDesktop; it is used to
// show the user where a desktop-space target falls on a capture.
func DesktopToNormalized(m contracts.CaptureMetadata, d contracts.Point) (contracts.Point, error) {
	if err := Validate(m); err != nil {
		return contracts.Point{}, err
	}
	u := (d.X - m.Bounds.Min.X) / m.Bounds.Width()
	v := (d.Y - m.Bounds.Min.Y) / m.Bounds.Height()
	switch quarterTurns(m.Rotation) {
	case 1:
		u, v = v, 1-u
	case 2:
		u, v = 1-u, 1-v
	case 3:
		u, v = 1-v, u
	}
	return contracts.Point{X: u, Y: v}, nil
}

// Target is a model point resolved against the live display topology.
type Target struct {
	Display contracts.Display
	Desktop contracts.Point
	// Local is display-local logical coordinates.
	Local contracts.Point
	// Surface is display-local native pixels, what an overlay surface
	// rendering at native resolution draws at.
	Surface contracts.Point
}

// Resolve maps a normalized capture point onto the current screen. It fails
// with contracts.ErrStale when the topology changed since the capture
// (FR-SCREEN-005): computer use must then refuse, and passive hints must
// recapture (PRD §9.7).
func Resolve(m contracts.CaptureMetadata, topo contracts.Topology, p contracts.Point) (Target, error) {
	if topo.Version != m.TopologyVersion {
		return Target{}, fmt.Errorf("%w: topology %d, capture taken at %d", contracts.ErrStale, topo.Version, m.TopologyVersion)
	}
	desktop, err := NormalizedToDesktop(m, p)
	if err != nil {
		return Target{}, err
	}
	d, ok := displayAt(topo, desktop, m.DisplayID)
	if !ok {
		return Target{}, fmt.Errorf("%w: no display at (%g,%g)", contracts.ErrStale, desktop.X, desktop.Y)
	}
	local := contracts.Point{X: desktop.X - d.Bounds.Min.X, Y: desktop.Y - d.Bounds.Min.Y}
	return Target{
		Display: d,
		Desktop: desktop,
		Local:   local,
		Surface: contracts.Point{X: local.X * d.Scale, Y: local.Y * d.Scale},
	}, nil
}

// displayAt prefers the capture's own display, then any display containing
// the point. Points on the far right/bottom edge of a full-display capture
// (normalized 1.0) are clamped into the display they belong to.
func displayAt(topo contracts.Topology, p contracts.Point, prefer contracts.DisplayID) (contracts.Display, bool) {
	if d, ok := topo.Display(prefer); ok && closedContains(d.Bounds, p) {
		return d, true
	}
	for _, d := range topo.Displays {
		if d.Bounds.Contains(p) {
			return d, true
		}
	}
	return contracts.Display{}, false
}

func closedContains(r contracts.Rect, p contracts.Point) bool {
	return p.X >= r.Min.X && p.X <= r.Max.X && p.Y >= r.Min.Y && p.Y <= r.Max.Y
}
