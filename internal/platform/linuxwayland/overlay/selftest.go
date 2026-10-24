//go:build linux

package overlay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xlsft/companion/internal/platform/linuxwayland/wl"
	"github.com/xlsft/companion/pkg/contracts"
)

const (
	layerTop  = 2
	btnLeft   = 0x110
	victimTag = "selftest-victim"
)

// ClickThroughReport is the outcome of SelfTestClickThrough.
type ClickThroughReport struct {
	// PassThrough: a click at the overlay's transparent centre reached the
	// surface below and the overlay saw no pointer event.
	PassThrough bool
	// Interactive: with SetInteractive(card) a click inside card reached the
	// overlay and not the surface below.
	Interactive bool
	// Restored: after SetInteractive(empty) the surface below receives the
	// pointer again.
	Restored bool
	Events   []PointerEvent
}

// HasVirtualPointer reports whether SelfTestClickThrough can run.
func (h *Host) HasVirtualPointer() bool { return h.vptrMgr != 0 && h.seat != 0 }

// SelfTestClickThrough proves click-through without a human: it maps a
// small opaque "victim" layer surface on the top layer (below the overlay)
// at the centre of s's output, drives a zwlr_virtual_pointer_v1 over it and
// clicks, then repeats inside card after SetInteractive(card). It moves
// the real cursor of the session, so it is for spikes and explicit
// diagnostics only.
func (h *Host) SelfTestClickThrough(ctx context.Context, os contracts.OverlaySurface, card contracts.Rect) (ClickThroughReport, error) {
	var rep ClickThroughReport
	s, ok := os.(*Surface)
	if !ok || s.h != h {
		return rep, errors.New("overlay: surface not from this host")
	}
	if !h.HasVirtualPointer() {
		return rep, contracts.Unsupported(contracts.CapInputInjection)
	}
	lw, lh := s.lw, s.lh
	if lw == 0 || lh == 0 {
		return rep, errors.New("overlay: surface not configured")
	}

	victim, err := h.newVictim(ctx, s.out, 1000, 400)
	if err != nil {
		return rep, err
	}
	defer victim.close()

	c := h.c
	vp := c.NewObject(nil)
	if err := c.Send(h.vptrMgr, 2, wl.NewMsg().Object(h.seat).Object(s.out.id).NewID(vp)); err != nil {
		return rep, err
	}
	defer func() {
		_ = c.Send(vp, 8, nil)
		c.Forget(vp)
	}()
	ms := func() uint32 { return uint32(time.Now().UnixMilli()) }
	move := func(x, y float64) {
		_ = c.Send(vp, 1, wl.NewMsg().Uint(ms()).Uint(uint32(x)).Uint(uint32(y)).Uint(lw).Uint(lh))
		_ = c.Send(vp, 4, nil) // frame
	}
	click := func() {
		_ = c.Send(vp, 2, wl.NewMsg().Uint(ms()).Uint(btnLeft).Uint(1))
		_ = c.Send(vp, 4, nil)
		_ = c.Send(vp, 2, wl.NewMsg().Uint(ms()).Uint(btnLeft).Uint(0))
		_ = c.Send(vp, 4, nil)
	}

	var mu sync.Mutex
	var phase []PointerEvent
	collect := func(d time.Duration) []PointerEvent {
		deadline := time.After(d)
		for {
			select {
			case pe := <-h.ptr:
				mu.Lock()
				phase = append(phase, pe)
				mu.Unlock()
			case <-deadline:
				mu.Lock()
				out := phase
				phase = nil
				mu.Unlock()
				rep.Events = append(rep.Events, out...)
				return out
			case <-ctx.Done():
				return nil
			}
		}
	}
	got := func(evs []PointerEvent, target string, kind PointerKind) bool {
		for _, e := range evs {
			if e.Target == target && e.Kind == kind {
				return true
			}
		}
		return false
	}
	cx, cy := float64(lw)/2, float64(lh)/2
	settle := 250 * time.Millisecond

	// Clicks are only sent once the pointer is known to be over one of our
	// own surfaces, so a failing test never clicks the user's windows.
	moveAndClick := func(x, y float64, want string) []PointerEvent {
		move(x, y)
		ev := collect(settle)
		if got(ev, want, PointerEnter) || h.focusTag() == want {
			click()
			ev = append(ev, collect(settle)...)
		}
		return ev
	}

	// Phase 1: overlay click-through over the victim.
	move(cx-5, cy-5)
	ev := moveAndClick(cx, cy, victimTag)
	rep.PassThrough = got(ev, victimTag, PointerButton) && !got(ev, "overlay", PointerEnter) && !got(ev, "overlay", PointerButton)

	// Phase 2: only the card is interactive.
	if !card.Empty() {
		if err := s.SetInteractive(card); err != nil {
			return rep, err
		}
		collect(100 * time.Millisecond)
		ev = moveAndClick((card.Min.X+card.Max.X)/2, (card.Min.Y+card.Max.Y)/2, "overlay")
		rep.Interactive = got(ev, "overlay", PointerButton) && !got(ev, victimTag, PointerButton)
		if err := s.SetInteractive(contracts.Rect{}); err != nil {
			return rep, err
		}
		collect(100 * time.Millisecond)
	}

	// Phase 3: back over the victim with click-through restored.
	ev = moveAndClick(cx, cy, victimTag)
	rep.Restored = got(ev, victimTag, PointerButton) && !got(ev, "overlay", PointerButton)
	return rep, ctx.Err()
}

// focusTag names the surface currently holding pointer focus.
func (h *Host) focusTag() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.surfaces[h.ptrFocus]; ok {
		return "overlay"
	}
	return h.tags[h.ptrFocus]
}

type victim struct {
	h                *Host
	surf, layer, buf uint32
	shm              *wl.ShmFile
}

// newVictim maps an opaque centred layer surface with a default (full)
// input region on the top layer, which sits below the overlay layer.
func (h *Host) newVictim(ctx context.Context, o *output, w, hh int) (*victim, error) {
	c := h.c
	v := &victim{h: h}
	v.surf = c.NewObject(nil)
	if err := c.Send(h.compositor, 0, wl.NewMsg().NewID(v.surf)); err != nil {
		return nil, err
	}
	conf := make(chan struct{})
	var once sync.Once
	v.layer = c.NewObject(func(op uint16, ev *wl.Event) {
		if op == 0 {
			serial := ev.Uint()
			_ = c.Send(v.layer, 6, wl.NewMsg().Uint(serial))
			once.Do(func() { close(conf) })
		}
	})
	_ = c.Send(h.layerShell, 0, wl.NewMsg().NewID(v.layer).Object(v.surf).Object(o.id).Uint(layerTop).String("openclick-selftest"))
	_ = c.Send(v.layer, 0, wl.NewMsg().Uint(uint32(w)).Uint(uint32(hh))) // set_size; no anchor = centred
	_ = c.Send(v.layer, 4, wl.NewMsg().Uint(keyboardNone))
	h.mu.Lock()
	h.tags[v.surf] = victimTag
	h.mu.Unlock()
	_ = c.Send(v.surf, 6, nil)
	select {
	case <-conf:
	case <-time.After(2 * time.Second):
		v.close()
		return nil, errors.New("overlay: victim surface not configured")
	case <-ctx.Done():
		v.close()
		return nil, ctx.Err()
	}
	stride := w * 4
	shm, err := wl.NewShmFile(stride * hh)
	if err != nil {
		v.close()
		return nil, err
	}
	v.shm = shm
	for i := 0; i < len(shm.Data); i += 4 { // opaque dark green, B,G,R,A
		shm.Data[i], shm.Data[i+1], shm.Data[i+2], shm.Data[i+3] = 0x30, 0x60, 0x20, 0xff
	}
	pool := c.NewObject(nil)
	_ = c.Send(h.shm, 0, wl.NewMsg().NewID(pool).Fd(shm.Fd).Int(int32(stride*hh)))
	v.buf = c.NewObject(nil)
	_ = c.Send(pool, 0, wl.NewMsg().NewID(v.buf).Int(0).Int(int32(w)).Int(int32(hh)).Int(int32(stride)).Uint(shmFormatARGB8888))
	_ = c.Send(pool, 1, nil)
	c.Forget(pool)
	_ = c.Send(v.surf, 1, wl.NewMsg().Object(v.buf).Int(0).Int(0))
	_ = c.Send(v.surf, 2, wl.NewMsg().Int(0).Int(0).Int(int32(w)).Int(int32(hh)))
	if err := c.Send(v.surf, 6, nil); err != nil {
		v.close()
		return nil, fmt.Errorf("overlay: victim commit: %w", err)
	}
	return v, c.Roundtrip(ctx)
}

func (v *victim) close() {
	c := v.h.c
	_ = c.Send(v.layer, 7, nil)
	_ = c.Send(v.surf, 0, nil)
	if v.buf != 0 {
		_ = c.Send(v.buf, 0, nil)
	}
	for _, id := range []uint32{v.layer, v.surf, v.buf} {
		if id != 0 {
			c.Forget(id)
		}
	}
	v.h.mu.Lock()
	delete(v.h.tags, v.surf)
	v.h.mu.Unlock()
	if v.shm != nil {
		v.shm.Close()
	}
}
