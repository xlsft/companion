package overlay

import (
	"context"
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/coordinates"
	"github.com/xlsft/companion/pkg/contracts"
)

var ErrUnknownCapture = errors.New("overlay command references unknown capture")

// TopologyFunc returns the current display topology.
type TopologyFunc func(ctx context.Context) (contracts.Topology, error)

// frame is a display's reusable frame buffer and the area its last
// render drew, which the next render clears and reports as damage.
type frame struct {
	img             *image.RGBA
	drawn, volatile image.Rectangle
	scene           string
}

// sceneKey identifies which annotations, in which incarnation, a frame
// shows; replacing an annotation gives it a new birth time.
func sceneKey(order []string, anns map[string]*annotation) string {
	var b strings.Builder
	for _, id := range order {
		fmt.Fprintf(&b, "%s@%d;", id, anns[id].shape.born.UnixNano())
	}
	return b.String()
}

type annotation struct {
	cmd     Command
	display contracts.DisplayID
	shape   shape
	cancel  func() bool // stops the lifetime timer
}

// Engine is the overlay session lifecycle (OC-50): it owns the scene of the
// current session, maps commands to screen space, presents frames and
// closes surfaces as soon as they have nothing to show, so no invisible
// input-blocking window is left behind.
type Engine struct {
	host     contracts.OverlayHost
	topology TopologyFunc
	// afterFunc is time.AfterFunc; replaced in tests.
	afterFunc func(time.Duration, func()) func() bool

	// now is time.Now; replaced in tests.
	now func() time.Time

	mu          sync.Mutex
	current     contracts.SessionRef
	captures    map[contracts.CaptureID]contracts.CaptureMetadata
	annotations map[string]*annotation
	surfaces    map[contracts.DisplayID]contracts.OverlaySurface
	// frames are reused per display: animating allocates no frame buffers.
	frames map[contracts.DisplayID]*frame
	// motion enables entrance and shimmer animation; animating is set
	// while the frame loop runs. The loop only runs while hints are shown.
	motion    bool
	animating bool
}

func NewEngine(host contracts.OverlayHost, topology TopologyFunc) *Engine {
	return &Engine{
		host:     host,
		topology: topology,
		afterFunc: func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		},
		now:         time.Now,
		captures:    map[contracts.CaptureID]contracts.CaptureMetadata{},
		annotations: map[string]*annotation{},
		surfaces:    map[contracts.DisplayID]contracts.OverlaySurface{},
		frames:      map[contracts.DisplayID]*frame{},
		motion:      true,
	}
}

// SetMotion turns animation on or off, e.g. for the system's reduced
// motion preference. Without motion hints appear in their final look.
func (e *Engine) SetMotion(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.motion = on
	e.animateLocked()
}

// animateLocked starts the frame loop if there is something to animate.
func (e *Engine) animateLocked() {
	if !e.motion || e.animating || len(e.annotations) == 0 {
		return
	}
	e.animating = true
	go e.animLoop()
}

// animLoop re-presents every display with hints at frameEvery until the
// scene is empty or motion is turned off. A frame that cannot be shown is
// skipped; the next tick tries again.
func (e *Engine) animLoop() {
	t := time.NewTicker(frameEvery)
	defer t.Stop()
	for range t.C {
		e.mu.Lock()
		if !e.motion || len(e.annotations) == 0 {
			e.animating = false
			e.mu.Unlock()
			return
		}
		shown := map[contracts.DisplayID]bool{}
		for _, a := range e.annotations {
			shown[a.display] = true
		}
		for d := range shown {
			_ = e.presentLocked(context.Background(), d)
		}
		e.mu.Unlock()
	}
}

// RegisterCapture makes a capture's metadata available to commands of the
// session that took it.
func (e *Engine) RegisterCapture(meta contracts.CaptureMetadata) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.captures[meta.ID] = meta
}

// Apply executes one command. Commands of an older generation or another
// session than the current one are dropped silently (FR-VOICE-008); a
// command of a newer session replaces the whole scene.
func (e *Engine) Apply(ctx context.Context, cmd Command) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.isStale(cmd.Session) {
		return nil
	}
	if cmd.Session != e.current {
		e.clearLocked(ctx)
		e.current = cmd.Session
	}

	switch cmd.Op {
	case OpClearSession:
		e.clearLocked(ctx)
		return nil
	case OpRemove:
		a, ok := e.annotations[cmd.ID]
		if !ok {
			return nil
		}
		e.removeLocked(cmd.ID)
		return e.presentLocked(ctx, a.display)
	}

	a, err := e.resolveLocked(ctx, cmd)
	if err != nil {
		return err
	}
	var touched []contracts.DisplayID
	if old, ok := e.annotations[cmd.ID]; ok {
		e.removeLocked(cmd.ID)
		touched = append(touched, old.display)
	}
	e.annotations[cmd.ID] = a
	if cmd.Lifetime > 0 {
		id, ref := cmd.ID, cmd.Session
		a.cancel = e.afterFunc(cmd.Lifetime, func() { e.expire(ref, id) })
	}
	if !slices.Contains(touched, a.display) {
		touched = append(touched, a.display)
	}
	var errs []error
	for _, d := range touched {
		errs = append(errs, e.presentLocked(ctx, d))
	}
	e.animateLocked()
	return errors.Join(errs...)
}

// SetSegment switches the scene to a new speech segment: annotations bound
// to other segments disappear (FR-OVERLAY-007).
func (e *Engine) SetSegment(ctx context.Context, ref contracts.SessionRef, segment int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref != e.current {
		return nil
	}
	touched := map[contracts.DisplayID]bool{}
	for id, a := range e.annotations {
		if a.cmd.Segment >= 0 && a.cmd.Segment != segment {
			touched[a.display] = true
			e.removeLocked(id)
		}
	}
	var errs []error
	for d := range touched {
		errs = append(errs, e.presentLocked(ctx, d))
	}
	return errors.Join(errs...)
}

// Clear removes everything and closes all surfaces, e.g. on Escape
// (FR-OVERLAY acceptance: nothing of the session stays visible).
func (e *Engine) Clear(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clearLocked(ctx)
	clear(e.captures)
}

// ForgetCapture drops metadata of a capture that is no longer referenced.
func (e *Engine) ForgetCapture(id contracts.CaptureID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.captures, id)
}

// isStale reports whether ref belongs to the current session's past.
func (e *Engine) isStale(ref contracts.SessionRef) bool {
	return ref.ID == e.current.ID && ref.Generation < e.current.Generation
}

func (e *Engine) expire(ref contracts.SessionRef, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.annotations[id]
	if !ok || ref != e.current || a.cmd.Session != ref {
		return
	}
	e.removeLocked(id)
	// A failed present after expiry has nobody to report to; the next
	// command or Clear retries and closes the surface.
	_ = e.presentLocked(context.Background(), a.display)
}

func (e *Engine) removeLocked(id string) {
	if a, ok := e.annotations[id]; ok {
		if a.cancel != nil {
			a.cancel()
		}
		delete(e.annotations, id)
	}
}

func (e *Engine) clearLocked(ctx context.Context) {
	for id := range e.annotations {
		e.removeLocked(id)
	}
	for d, s := range e.surfaces {
		e.closeSurface(d, s)
	}
}

// closeSurface unmaps the window entirely; an empty scene must leave no
// window that could intercept input.
func (e *Engine) closeSurface(d contracts.DisplayID, s contracts.OverlaySurface) {
	_ = s.Close()
	delete(e.surfaces, d)
	delete(e.frames, d)
}

func (e *Engine) resolveLocked(ctx context.Context, cmd Command) (*annotation, error) {
	meta, ok := e.captures[cmd.Capture]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownCapture, cmd.Capture)
	}
	topo, err := e.topology(ctx)
	if err != nil {
		return nil, fmt.Errorf("overlay topology: %w", err)
	}
	pts := make([]contracts.Point, len(cmd.Points))
	var display contracts.Display
	for i, p := range cmd.Points {
		t, err := coordinates.Resolve(meta, topo, p)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			display = t.Display
		}
		// Shapes are drawn on the display of their first point; later
		// points are expressed relative to that display even if they fall
		// outside it, and the frame clips them.
		pts[i] = contracts.Point{
			X: (t.Desktop.X - display.Bounds.Min.X) * display.Scale,
			Y: (t.Desktop.Y - display.Bounds.Min.Y) * display.Scale,
		}
	}
	text := cmd.Text
	if cmd.Kind == KindImageCard && text == "" {
		text = cmd.ImageRef
	}
	return &annotation{
		cmd:     cmd,
		display: display.ID,
		shape: shape{
			kind:   cmd.Kind,
			pts:    pts,
			radius: cmd.Radius * meta.Bounds.Width() * display.Scale,
			text:   text,
			style:  cmd.Style,
			scale:  display.Scale,
			born:   e.now(),
			cache:  &shapeCache{},
		},
	}, nil
}

// presentLocked redraws one display; an empty scene closes its surface.
func (e *Engine) presentLocked(ctx context.Context, id contracts.DisplayID) error {
	var shapes []shape
	var order []string
	for aid, a := range e.annotations {
		if a.display == id {
			order = append(order, aid)
		}
	}
	slices.Sort(order) // deterministic z-order
	for _, aid := range order {
		shapes = append(shapes, e.annotations[aid].shape)
	}

	s, open := e.surfaces[id]
	if len(shapes) == 0 {
		if open {
			e.closeSurface(id, s)
		}
		return nil
	}
	topo, err := e.topology(ctx)
	if err != nil {
		return fmt.Errorf("overlay topology: %w", err)
	}
	d, ok := topo.Display(id)
	if !ok {
		// Display unplugged: its hints are void (PRD §13).
		for _, aid := range order {
			e.removeLocked(aid)
		}
		if open {
			e.closeSurface(id, s)
		}
		return fmt.Errorf("%w: display %s gone", contracts.ErrStale, id)
	}
	full := !open
	if !open {
		s, err = e.host.Open(ctx, id)
		if err != nil {
			return fmt.Errorf("open overlay on %s: %w", id, err)
		}
		e.surfaces[id] = s
	}
	fr := e.frames[id]
	if fr == nil || fr.img.Bounds().Dx() != d.Native.W || fr.img.Bounds().Dy() != d.Native.H {
		fr = &frame{img: image.NewRGBA(image.Rect(0, 0, d.Native.W, d.Native.H))}
		e.frames[id] = fr
		full = true
	}
	drawn, volatile := renderInto(fr.img, shapes, e.now(), e.motion, fr.drawn)
	// Unchanged scene: only the moving parts differ from the last frame
	// (and where they were). A changed scene may differ anywhere either
	// frame drew.
	damage := volatile.Union(fr.volatile)
	if key := sceneKey(order, e.annotations); key != fr.scene {
		damage = drawn.Union(fr.drawn)
		fr.scene = key
	}
	fr.drawn, fr.volatile = drawn, volatile
	if dp, ok := s.(contracts.OverlayDamagePresenter); ok && !full {
		if damage.Empty() {
			return nil
		}
		return dp.PresentDamage(ctx, fr.img, damage)
	}
	return s.Present(ctx, fr.img)
}
