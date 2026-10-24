// Package capture picks the screen capture backend for the running session
// and implements contracts.ScreenCapturer on top of it (OC-40). Backends
// only grab pixels for a display area; target resolution, metadata and
// topology bookkeeping are shared here so every backend reports the same
// CaptureMetadata (PRD §9.7).
package capture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"maps"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/xlsft/companion/internal/coordinates"
	"github.com/xlsft/companion/pkg/contracts"
)

// TopologySource is the part of the displays Tracker capture needs.
type TopologySource interface {
	Topology(ctx context.Context) (contracts.Topology, error)
}

// ActiveDisplay decides which display CaptureActiveDisplay means. An error
// or an ID missing from topo makes the Capturer try the next resolver and
// finally the primary display.
type ActiveDisplay interface {
	Active(ctx context.Context, topo contracts.Topology) (contracts.DisplayID, error)
}

// ActiveFunc adapts a function to ActiveDisplay.
type ActiveFunc func(ctx context.Context, topo contracts.Topology) (contracts.DisplayID, error)

func (f ActiveFunc) Active(ctx context.Context, topo contracts.Topology) (contracts.DisplayID, error) {
	return f(ctx, topo)
}

// target is one area to grab, always within a single display.
type target struct {
	display contracts.Display
	// area is in desktop logical coordinates, integral and inside
	// display.Bounds.
	area contracts.Rect
	full bool // area is the whole display
}

// shot is what a backend returns for one target.
type shot struct {
	img *image.RGBA
	// scale is native pixels per logical pixel of img.
	scale float64
	// rotation turns img into what is shown on screen, clockwise degrees.
	rotation int
	ids      map[string]string
	at       time.Time
}

// grabber is a capture backend.
type grabber interface {
	grab(ctx context.Context, topo contracts.Topology, ts []target) ([]shot, error)
	close() error
}

// Capturer implements contracts.ScreenCapturer.
type Capturer struct {
	backend string
	topo    TopologySource
	active  []ActiveDisplay
	g       grabber
}

var _ contracts.ScreenCapturer = (*Capturer)(nil)

// Backend names the mechanism in use, as in CapabilityState.Backend.
func (c *Capturer) Backend() string { return c.backend }

// SetActive replaces the resolvers for CaptureActiveDisplay; they are
// tried in order before falling back to the primary display.
func (c *Capturer) SetActive(rs ...ActiveDisplay) { c.active = rs }

// Close releases the backend.
func (c *Capturer) Close() error { return c.g.close() }

// Capture grabs what req asks for. Captures stay in memory (FR-SCREEN-006).
func (c *Capturer) Capture(ctx context.Context, req contracts.CaptureRequest) ([]*contracts.Capture, error) {
	// One retry absorbs a topology change that raced the grab; a second
	// one in a row means the screen is still settling.
	for attempt := 0; ; attempt++ {
		caps, err := c.capture(ctx, req)
		if errors.Is(err, errTopologyMoved) && attempt == 0 {
			continue
		}
		if errors.Is(err, errTopologyMoved) {
			return nil, fmt.Errorf("%w: display topology changed during capture", contracts.ErrStale)
		}
		return caps, err
	}
}

var errTopologyMoved = errors.New("topology moved")

func (c *Capturer) capture(ctx context.Context, req contracts.CaptureRequest) ([]*contracts.Capture, error) {
	topo, err := c.topo.Topology(ctx)
	if err != nil {
		return nil, fmt.Errorf("capture: topology: %w", err)
	}
	if len(topo.Displays) == 0 {
		return nil, fmt.Errorf("%w: no displays", contracts.ErrStale)
	}
	ts, err := resolve(ctx, req, topo, c.active)
	if err != nil {
		return nil, err
	}
	shots, err := c.g.grab(ctx, topo, ts)
	if err != nil {
		return nil, err
	}
	if len(shots) != len(ts) {
		return nil, fmt.Errorf("capture: backend returned %d images for %d targets", len(shots), len(ts))
	}
	after, err := c.topo.Topology(ctx)
	if err != nil {
		return nil, fmt.Errorf("capture: topology: %w", err)
	}
	if after.Version != topo.Version {
		return nil, errTopologyMoved
	}
	origin := desktopOrigin(topo)
	out := make([]*contracts.Capture, len(ts))
	for i, t := range ts {
		m := metadata(c.backend, topo, origin, t, shots[i])
		if err := coordinates.Validate(m); err != nil {
			return nil, fmt.Errorf("capture: %s: %w", c.backend, err)
		}
		out[i] = &contracts.Capture{Meta: m, Image: shots[i].img}
	}
	return out, nil
}

func metadata(backend string, topo contracts.Topology, origin contracts.Point, t target, s shot) contracts.CaptureMetadata {
	ids := map[string]string{"backend": backend}
	maps.Copy(ids, s.ids)
	ts := s.at
	if ts.IsZero() {
		ts = time.Now()
	}
	b := s.img.Bounds()
	return contracts.CaptureMetadata{
		ID:              newID(),
		DisplayID:       t.display.ID,
		Native:          contracts.PixelSize{W: b.Dx(), H: b.Dy()},
		Bounds:          t.area,
		Scale:           s.scale,
		Rotation:        s.rotation,
		DesktopOrigin:   origin,
		TopologyVersion: topo.Version,
		Timestamp:       ts,
		PlatformIDs:     ids,
	}
}

var (
	idPrefix = func() string {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}()
	idSeq atomic.Uint64
)

// newID is unique within the process; the random prefix keeps IDs from
// different runs apart in exported diagnostics.
func newID() contracts.CaptureID {
	return contracts.CaptureID("cap-" + idPrefix + "-" + strconv.FormatUint(idSeq.Add(1), 10))
}

// desktopOrigin is the top-left corner of the union of all displays.
func desktopOrigin(topo contracts.Topology) contracts.Point {
	b := desktopBounds(topo)
	return b.Min
}

func desktopBounds(topo contracts.Topology) contracts.Rect {
	var u contracts.Rect
	for i, d := range topo.Displays {
		if i == 0 {
			u = d.Bounds
			continue
		}
		u.Min.X, u.Min.Y = min(u.Min.X, d.Bounds.Min.X), min(u.Min.Y, d.Bounds.Min.Y)
		u.Max.X, u.Max.Y = max(u.Max.X, d.Bounds.Max.X), max(u.Max.Y, d.Bounds.Max.Y)
	}
	return u
}

// resolve turns a request into per-display targets.
func resolve(ctx context.Context, req contracts.CaptureRequest, topo contracts.Topology, active []ActiveDisplay) ([]target, error) {
	switch req.Target {
	case contracts.CaptureActiveDisplay:
		return []target{whole(activeDisplay(ctx, topo, active))}, nil
	case contracts.CaptureAllDisplays:
		ts := make([]target, len(topo.Displays))
		for i, d := range topo.Displays {
			ts[i] = whole(d)
		}
		return ts, nil
	case contracts.CaptureDisplay:
		d, ok := topo.Display(req.Display)
		if !ok {
			return nil, fmt.Errorf("%w: display %q not connected", contracts.ErrStale, req.Display)
		}
		return []target{whole(d)}, nil
	case contracts.CaptureRegion:
		return regionTarget(topo, req.Region)
	}
	return nil, fmt.Errorf("capture: unknown target %d", req.Target)
}

func whole(d contracts.Display) target { return target{display: d, area: d.Bounds, full: true} }

// regionTarget clips a region to the display it overlaps most, so the
// result keeps one DisplayID, one scale and one rotation. Edges are rounded
// outward to whole logical pixels, which every backend can address.
func regionTarget(topo contracts.Topology, r contracts.Rect) ([]target, error) {
	if r.Empty() {
		return nil, errors.New("capture: empty region")
	}
	r = contracts.Rect{
		Min: contracts.Point{X: math.Floor(r.Min.X), Y: math.Floor(r.Min.Y)},
		Max: contracts.Point{X: math.Ceil(r.Max.X), Y: math.Ceil(r.Max.Y)},
	}
	best, bestArea := -1, 0.0
	for i, d := range topo.Displays {
		in := d.Bounds.Intersect(r)
		if a := in.Width() * in.Height(); a > bestArea {
			best, bestArea = i, a
		}
	}
	if best < 0 {
		return nil, errors.New("capture: region is outside every display")
	}
	d := topo.Displays[best]
	area := d.Bounds.Intersect(r)
	return []target{{display: d, area: area, full: area == d.Bounds}}, nil
}

func activeDisplay(ctx context.Context, topo contracts.Topology, rs []ActiveDisplay) contracts.Display {
	for _, r := range rs {
		id, err := r.Active(ctx, topo)
		if err != nil {
			continue
		}
		if d, ok := topo.Display(id); ok {
			return d
		}
	}
	return primary(topo)
}

func primary(topo contracts.Topology) contracts.Display {
	for _, d := range topo.Displays {
		if d.Primary {
			return d
		}
	}
	return topo.Displays[0]
}

// displayAt finds the display containing p, for pointer-based resolvers.
func displayAt(topo contracts.Topology, p contracts.Point) (contracts.DisplayID, error) {
	for _, d := range topo.Displays {
		if d.Bounds.Contains(p) {
			return d.ID, nil
		}
	}
	return "", errors.New("capture: point outside every display")
}

// bufferRotation reports how a screencopy buffer is oriented. The protocol
// delivers buffers untransformed, so the output's rotation still applies;
// a buffer that already has the on-screen aspect of a rotated output was
// transformed by the compositor and needs none. 180° cannot be told apart
// by size and follows the protocol.
func bufferRotation(t target, w, h int) int {
	rot := t.display.Rotation
	if rot != 90 && rot != 270 {
		return rot
	}
	sw, sh := t.area.Width()*t.display.Scale, t.area.Height()*t.display.Scale
	onScreen := math.Abs(float64(w)-sw) <= 1.5 && math.Abs(float64(h)-sh) <= 1.5
	swapped := math.Abs(float64(w)-sh) <= 1.5 && math.Abs(float64(h)-sw) <= 1.5
	if onScreen && !swapped {
		return 0
	}
	return rot
}

// deviceRect converts a target area to whole pixels for backends whose
// desktop coordinates are device pixels (X11).
func deviceRect(r contracts.Rect) image.Rectangle {
	return image.Rect(int(math.Round(r.Min.X)), int(math.Round(r.Min.Y)),
		int(math.Round(r.Max.X)), int(math.Round(r.Max.Y)))
}

// localRect converts a target area to display-local logical pixels.
func localRect(t target) image.Rectangle {
	o := t.display.Bounds.Min
	return image.Rect(
		int(math.Round(t.area.Min.X-o.X)), int(math.Round(t.area.Min.Y-o.Y)),
		int(math.Round(t.area.Max.X-o.X)), int(math.Round(t.area.Max.Y-o.Y)),
	)
}
