// Package overlay defines typed overlay commands and drives overlay
// surfaces. Models never emit text tags for drawing; they call tools that
// are converted into these commands (FR-OVERLAY-004).
package overlay

import (
	"errors"
	"fmt"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Kind is an overlay primitive (FR-OVERLAY-003).
type Kind string

const (
	KindPoint     Kind = "point"      // virtual cursor moves to Points[0]
	KindRing      Kind = "ring"       // target ring centered at Points[0]
	KindRect      Kind = "rect"       // Points[0] top-left, Points[1] bottom-right
	KindCircle    Kind = "circle"     // centered at Points[0] with Radius
	KindArrow     Kind = "arrow"      // from Points[0] to Points[1]
	KindFreehand  Kind = "freehand"   // polyline through Points, at least 2
	KindLabel     Kind = "label"      // Text anchored at Points[0]
	KindImageCard Kind = "image_card" // card anchored at Points[0] showing ImageRef
)

// Op is what a command does to the scene.
type Op string

const (
	OpShow         Op = "show"          // add or replace the annotation with ID
	OpRemove       Op = "remove"        // remove the annotation with ID
	OpClearSession Op = "clear_session" // remove everything of Session
)

// Style is optional presentation; zero values mean theme defaults.
type Style struct {
	Color       string  // "#RRGGBB" or "#RRGGBBAA"
	StrokeWidth float64 // logical pixels
	Emphasis    bool    // pulse/glow
}

// Command is one overlay instruction. Points are in normalized capture
// coordinates of Capture and are mapped to the screen by the
// CoordinateMapper before drawing (FR-OVERLAY-005, FR-OVERLAY-006).
type Command struct {
	Op      Op
	ID      string
	Session contracts.SessionRef
	Capture contracts.CaptureID

	Kind   Kind
	Points []contracts.Point
	// Radius for KindCircle and KindRing, as a fraction of capture width.
	Radius   float64
	Text     string
	ImageRef string
	Style    Style

	// Lifetime of 0 keeps the annotation until removed or the session ends.
	Lifetime time.Duration
	// Segment links the annotation to a speech segment so it can be swapped
	// when the next segment starts playing (FR-OVERLAY-007); -1 for none.
	Segment int
}

var ErrInvalidCommand = errors.New("invalid overlay command")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCommand, fmt.Sprintf(format, args...))
}

// Validate checks the command shape. It does not check staleness; that
// needs the current display topology and is done by the mapper.
func (c Command) Validate() error {
	if c.Session.ID == "" {
		return invalid("missing session")
	}
	switch c.Op {
	case OpClearSession:
		return nil
	case OpRemove:
		if c.ID == "" {
			return invalid("remove without id")
		}
		return nil
	case OpShow:
	default:
		return invalid("unknown op %q", c.Op)
	}
	if c.ID == "" {
		return invalid("show without id")
	}
	if c.Capture == "" {
		return invalid("show without capture")
	}
	for i, p := range c.Points {
		if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
			return invalid("point %d (%g,%g) outside normalized capture", i, p.X, p.Y)
		}
	}
	if c.Lifetime < 0 {
		return invalid("negative lifetime")
	}
	want, ok := pointsPerKind[c.Kind]
	if !ok {
		return invalid("unknown kind %q", c.Kind)
	}
	if want > 0 && len(c.Points) != want {
		return invalid("%s needs %d points, got %d", c.Kind, want, len(c.Points))
	}
	switch c.Kind {
	case KindFreehand:
		if len(c.Points) < 2 {
			return invalid("freehand needs at least 2 points")
		}
	case KindCircle, KindRing:
		if c.Radius <= 0 || c.Radius > 1 {
			return invalid("%s radius %g outside (0,1]", c.Kind, c.Radius)
		}
	case KindLabel:
		if c.Text == "" {
			return invalid("label without text")
		}
	case KindImageCard:
		if c.ImageRef == "" {
			return invalid("image card without image")
		}
	case KindRect:
		if c.Points[0].X >= c.Points[1].X || c.Points[0].Y >= c.Points[1].Y {
			return invalid("rect corners out of order")
		}
	}
	return nil
}

// pointsPerKind is the exact point count per kind; 0 means variable.
var pointsPerKind = map[Kind]int{
	KindPoint:     1,
	KindRing:      1,
	KindRect:      2,
	KindCircle:    1,
	KindArrow:     2,
	KindFreehand:  0,
	KindLabel:     1,
	KindImageCard: 1,
}
