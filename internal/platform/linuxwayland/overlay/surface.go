//go:build linux

package overlay

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"time"

	"github.com/xlsft/openclick/internal/platform/linuxwayland/wl"
	"github.com/xlsft/openclick/internal/platform/pixconv"
	"github.com/xlsft/openclick/pkg/contracts"
)

const (
	layerOverlay      = 3
	anchorAll         = 1 | 2 | 4 | 8 // top | bottom | left | right
	keyboardNone      = 0
	shmFormatARGB8888 = 0
	maxBuffers        = 3
	namespace         = "openclick-overlay"
)

// Surface is one layer-shell overlay on one output.
type Surface struct {
	h       *Host
	out     *output
	display contracts.DisplayID

	surf, layer, frac, viewport uint32

	configured chan struct{}
	confOnce   sync.Once

	mu          sync.Mutex
	lw, lh      uint32 // logical size from configure
	scale120    uint32 // wp_fractional_scale_v1 preferred scale, 0 if unknown
	closed      bool
	bufs        []*buffer
	lastLatency time.Duration
}

type buffer struct {
	id   uint32
	shm  *wl.ShmFile
	w, h int
	busy bool
}

func (h *Host) newSurface(o *output) (*Surface, error) {
	s := &Surface{h: h, out: o, display: o.displayID(), configured: make(chan struct{})}
	c := h.c
	s.surf = c.NewObject(nil)
	send := func(id uint32, op uint16, m *wl.Msg) error { return c.Send(id, op, m) }
	if err := send(h.compositor, 0, wl.NewMsg().NewID(s.surf)); err != nil {
		return nil, err
	}
	if h.HasFractionalScale() {
		s.frac = c.NewObject(s.handleFrac)
		_ = send(h.fracMgr, 1, wl.NewMsg().NewID(s.frac).Object(s.surf))
		s.viewport = c.NewObject(nil)
		_ = send(h.viewporter, 1, wl.NewMsg().NewID(s.viewport).Object(s.surf))
	}
	s.layer = c.NewObject(s.handleLayer)
	if err := send(h.layerShell, 0, wl.NewMsg().NewID(s.layer).Object(s.surf).Object(o.id).
		Uint(layerOverlay).String(namespace)); err != nil {
		return nil, err
	}
	_ = send(s.layer, 0, wl.NewMsg().Uint(0).Uint(0))    // set_size: follow anchors
	_ = send(s.layer, 1, wl.NewMsg().Uint(anchorAll))    // set_anchor
	_ = send(s.layer, 2, wl.NewMsg().Int(-1))            // set_exclusive_zone: ignore panels
	_ = send(s.layer, 4, wl.NewMsg().Uint(keyboardNone)) // set_keyboard_interactivity
	h.mu.Lock()
	h.surfaces[s.surf] = s
	h.mu.Unlock()
	if err := s.setInputRegion(contracts.Rect{}); err != nil {
		return nil, err
	}
	// Initial commit without a buffer asks the compositor for a configure.
	if err := send(s.surf, 6, nil); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Surface) handleLayer(op uint16, ev *wl.Event) {
	switch op {
	case 0: // configure(serial, width, height)
		serial, w, h := ev.Uint(), ev.Uint(), ev.Uint()
		s.mu.Lock()
		s.lw, s.lh = w, h
		s.mu.Unlock()
		_ = s.h.c.Send(s.layer, 6, wl.NewMsg().Uint(serial)) // ack_configure
		s.confOnce.Do(func() { close(s.configured) })
	case 1: // closed: output gone or compositor revoked the surface
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}
}

func (s *Surface) handleFrac(op uint16, ev *wl.Event) {
	if op == 0 {
		v := ev.Uint()
		s.mu.Lock()
		s.scale120 = v
		s.mu.Unlock()
	}
}

func (s *Surface) waitConfigured(ctx context.Context) error {
	select {
	case <-s.configured:
		return nil
	case <-s.h.c.Done():
		return s.h.c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Surface) Display() contracts.DisplayID { return s.display }

// Scale is native pixels per logical pixel: the fractional preferred scale
// when the compositor offers it, otherwise wl_output.scale.
func (s *Surface) Scale() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scaleLocked()
}

func (s *Surface) scaleLocked() float64 {
	if s.scale120 > 0 {
		return float64(s.scale120) / 120
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	return scaleFromOutput(s.out)
}

// LogicalSize is the configured surface size in logical pixels.
func (s *Surface) LogicalSize() contracts.Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return contracts.Size{W: float64(s.lw), H: float64(s.lh)}
}

// NativeSize is the frame size Present expects.
func (s *Surface) NativeSize() contracts.PixelSize {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.scaleLocked()
	return contracts.PixelSize{W: roundScaled(s.lw, sc), H: roundScaled(s.lh, sc)}
}

// LastFrameLatency is the time from the last commit to its wl_callback.done,
// i.e. until the compositor used the frame for a repaint.
func (s *Surface) LastFrameLatency() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLatency
}

// Present copies frame into a shm buffer and commits it. It does not wait
// for the frame callback; LastFrameLatency reports it afterwards.
func (s *Surface) Present(ctx context.Context, frame *image.RGBA) error {
	if err := s.waitConfigured(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fw, fh := frame.Bounds().Dx(), frame.Bounds().Dy()
	if fw <= 0 || fh <= 0 {
		return errors.New("overlay: empty frame")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	buf, err := s.freeBufferLocked(fw, fh)
	if err != nil {
		return err
	}
	pixconv.RGBAToBGRA(buf.shm.Data, fw*4, frame)
	buf.busy = true

	c := s.h.c
	_ = c.Send(s.surf, 1, wl.NewMsg().Object(buf.id).Int(0).Int(0)) // attach
	switch {
	case s.viewport != 0:
		// Buffer is in native pixels; the viewport maps it onto the logical
		// size, which is how fractional scales are expressed.
		_ = c.Send(s.viewport, 2, wl.NewMsg().Int(int32(s.lw)).Int(int32(s.lh)))
	case s.h.compositorVer >= 3 && s.lw > 0:
		bs := int32(max(1, math.Round(float64(fw)/float64(s.lw))))
		_ = c.Send(s.surf, 8, wl.NewMsg().Int(bs)) // set_buffer_scale
	}
	if s.h.compositorVer >= 4 {
		_ = c.Send(s.surf, 9, wl.NewMsg().Int(0).Int(0).Int(int32(fw)).Int(int32(fh))) // damage_buffer
	} else {
		_ = c.Send(s.surf, 2, wl.NewMsg().Int(0).Int(0).Int(math.MaxInt32).Int(math.MaxInt32))
	}
	start := time.Now()
	var once sync.Once
	cb := c.NewObject(func(op uint16, ev *wl.Event) {
		once.Do(func() {
			d := time.Since(start)
			s.mu.Lock()
			s.lastLatency = d
			s.mu.Unlock()
		})
	})
	_ = c.Send(s.surf, 3, wl.NewMsg().NewID(cb)) // frame
	return c.Send(s.surf, 6, nil)                // commit
}

func (s *Surface) freeBufferLocked(w, h int) (*buffer, error) {
	kept := s.bufs[:0]
	var free *buffer
	for _, b := range s.bufs {
		switch {
		case !b.busy && (b.w != w || b.h != h):
			s.destroyBuffer(b)
		case !b.busy && free == nil:
			free = b
			kept = append(kept, b)
		default:
			kept = append(kept, b)
		}
	}
	s.bufs = kept
	if free != nil {
		return free, nil
	}
	if len(s.bufs) >= maxBuffers {
		return nil, errors.New("overlay: all buffers still held by the compositor")
	}
	b, err := s.newBuffer(w, h)
	if err != nil {
		return nil, err
	}
	s.bufs = append(s.bufs, b)
	return b, nil
}

func (s *Surface) newBuffer(w, h int) (*buffer, error) {
	stride := w * 4
	size := stride * h
	shm, err := wl.NewShmFile(size)
	if err != nil {
		return nil, err
	}
	c := s.h.c
	pool := c.NewObject(nil)
	if err := c.Send(s.h.shm, 0, wl.NewMsg().NewID(pool).Fd(shm.Fd).Int(int32(size))); err != nil {
		shm.Close()
		return nil, err
	}
	b := &buffer{shm: shm, w: w, h: h}
	b.id = c.NewObject(func(op uint16, ev *wl.Event) {
		if op == 0 { // release
			s.mu.Lock()
			b.busy = false
			s.mu.Unlock()
		}
	})
	_ = c.Send(pool, 0, wl.NewMsg().NewID(b.id).Int(0).Int(int32(w)).Int(int32(h)).Int(int32(stride)).Uint(shmFormatARGB8888))
	// The buffer keeps the pool's memory alive; the pool object is not needed.
	_ = c.Send(pool, 1, nil)
	c.Forget(pool)
	return b, nil
}

func (s *Surface) destroyBuffer(b *buffer) {
	_ = s.h.c.Send(b.id, 0, nil)
	s.h.c.Forget(b.id)
	b.shm.Close()
}

// SetInteractive makes only region receive pointer input; an empty region
// restores full click-through. Keyboard interactivity stays none.
func (s *Surface) SetInteractive(region contracts.Rect) error {
	if err := s.setInputRegion(region); err != nil {
		return err
	}
	return s.h.c.Send(s.surf, 6, nil) // commit
}

func (s *Surface) setInputRegion(r contracts.Rect) error {
	c := s.h.c
	reg := c.NewObject(nil)
	if err := c.Send(s.h.compositor, 1, wl.NewMsg().NewID(reg)); err != nil {
		return err
	}
	if !r.Empty() {
		x0, y0 := math.Floor(r.Min.X), math.Floor(r.Min.Y)
		x1, y1 := math.Ceil(r.Max.X), math.Ceil(r.Max.Y)
		_ = c.Send(reg, 1, wl.NewMsg().Int(int32(x0)).Int(int32(y0)).Int(int32(x1-x0)).Int(int32(y1-y0)))
	}
	_ = c.Send(s.surf, 5, wl.NewMsg().Object(reg)) // set_input_region
	err := c.Send(reg, 0, nil)                     // destroy
	c.Forget(reg)
	return err
}

func (s *Surface) Close() error {
	s.mu.Lock()
	if s.surf == 0 {
		s.mu.Unlock()
		return nil
	}
	c := s.h.c
	_ = c.Send(s.layer, 7, nil)
	if s.viewport != 0 {
		_ = c.Send(s.viewport, 0, nil)
	}
	if s.frac != 0 {
		_ = c.Send(s.frac, 0, nil)
	}
	_ = c.Send(s.surf, 0, nil)
	for _, id := range []uint32{s.layer, s.viewport, s.frac, s.surf} {
		if id != 0 {
			c.Forget(id)
		}
	}
	for _, b := range s.bufs {
		s.destroyBuffer(b)
	}
	s.bufs = nil
	surf := s.surf
	s.surf = 0
	s.closed = true
	s.mu.Unlock()

	s.h.mu.Lock()
	delete(s.h.surfaces, surf)
	s.h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Roundtrip(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("overlay: close: %w", err)
	}
	return nil
}

var _ contracts.OverlaySurface = (*Surface)(nil)
var _ contracts.OverlayHost = (*Host)(nil)
