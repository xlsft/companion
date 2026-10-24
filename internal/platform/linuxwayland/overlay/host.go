//go:build linux

// Package overlay implements contracts.OverlayHost on Wayland compositors
// with zwlr_layer_shell_v1 (niri, Sway, Hyprland, KDE, wlroots-based).
// Each surface is an overlay-layer layer surface bound to one wl_output,
// with an empty input region (click-through) and no keyboard focus.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/xlsft/openclick/internal/platform/linuxwayland/wl"
	"github.com/xlsft/openclick/pkg/contracts"
)

// Backend is the CapabilityState.Backend name of this host.
const Backend = "wlr-layer-shell"

// PointerKind is the wl_pointer event behind a PointerEvent.
type PointerKind string

const (
	PointerEnter  PointerKind = "enter"
	PointerLeave  PointerKind = "leave"
	PointerButton PointerKind = "button"
)

// PointerEvent reports wl_pointer activity on a surface of this host. With
// an empty input region the compositor must never send these for an
// overlay; the spike and diagnostics use them to prove click-through.
type PointerEvent struct {
	Time time.Time
	// Target is "overlay" or the tag of a diagnostic surface.
	Target  string
	Display contracts.DisplayID
	Kind    PointerKind
	X, Y    float64 // surface-local, enter only
	Button  uint32  // linux evdev code, button only
	Pressed bool
}

// Host owns one Wayland connection shared by all its overlay surfaces.
type Host struct {
	c        *wl.Conn
	registry uint32

	compositor, shm, layerShell, fracMgr, viewporter, seat, pointer uint32
	compositorVer                                                   uint32

	mu       sync.Mutex
	globals  map[uint32]global
	outputs  map[uint32]*output // by registry name
	surfaces map[uint32]*Surface
	tags     map[uint32]string // diagnostic surfaces by wl_surface id
	ptrFocus uint32
	ptr      chan PointerEvent
	vptrMgr  uint32
}

type global struct {
	iface   string
	version uint32
}

type output struct {
	gname, id     uint32
	name, desc    string
	x, y          int32
	w, h          int32 // current mode, physical pixels
	transform     int32
	scale         int32
	make_, model_ string
	gone          bool
}

func (o *output) displayID() contracts.DisplayID {
	if o.name != "" {
		return contracts.DisplayID("wl:" + o.name)
	}
	return contracts.DisplayID("wl:output-" + strconv.FormatUint(uint64(o.gname), 10))
}

// New connects to the compositor. It returns contracts.Unsupported when the
// compositor has no layer-shell (e.g. GNOME/Mutter); callers then use the
// fallback panel.
func New(ctx context.Context) (*Host, error) {
	c, err := wl.Connect()
	if err != nil {
		return nil, &contracts.CapabilityError{Capability: contracts.CapOverlayClickThrough, Err: err}
	}
	h := &Host{
		c:        c,
		globals:  map[uint32]global{},
		outputs:  map[uint32]*output{},
		surfaces: map[uint32]*Surface{},
		tags:     map[uint32]string{},
		ptr:      make(chan PointerEvent, 64),
	}
	reg := c.NewObject(h.handleRegistry)
	h.registry = reg
	if err := c.Send(wl.DisplayID, 1, wl.NewMsg().NewID(reg)); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.Roundtrip(ctx); err != nil {
		c.Close()
		return nil, err
	}
	h.mu.Lock()
	for gname, g := range h.globals {
		switch g.iface {
		case "wl_compositor":
			h.compositorVer = min(g.version, 4)
			h.compositor = h.bind(reg, gname, g, h.compositorVer, nil)
		case "wl_shm":
			h.shm = h.bind(reg, gname, g, 1, nil)
		case "zwlr_layer_shell_v1":
			h.layerShell = h.bind(reg, gname, g, min(g.version, 4), nil)
		case "wp_fractional_scale_manager_v1":
			h.fracMgr = h.bind(reg, gname, g, 1, nil)
		case "wp_viewporter":
			h.viewporter = h.bind(reg, gname, g, 1, nil)
		case "zwlr_virtual_pointer_manager_v1":
			h.vptrMgr = h.bind(reg, gname, g, min(g.version, 2), nil)
		case "wl_seat":
			if h.seat == 0 {
				h.seat = h.bind(reg, gname, g, min(g.version, 3), h.handleSeat)
			}
		}
	}
	h.mu.Unlock()
	// Second roundtrip delivers wl_output and wl_seat initial state.
	if err := c.Roundtrip(ctx); err != nil {
		c.Close()
		return nil, err
	}
	if h.compositor == 0 || h.shm == 0 {
		c.Close()
		return nil, contracts.Unsupported(contracts.CapOverlayClickThrough)
	}
	if h.layerShell == 0 {
		c.Close()
		return nil, &contracts.CapabilityError{
			Capability: contracts.CapOverlayClickThrough,
			Err:        fmt.Errorf("%w: compositor has no zwlr_layer_shell_v1", contracts.ErrUnsupported),
		}
	}
	return h, nil
}

// bind must be called with h.mu held.
func (h *Host) bind(reg, gname uint32, g global, version uint32, hd wl.Handler) uint32 {
	id := h.c.NewObject(hd)
	_ = h.c.Send(reg, 0, wl.NewMsg().Uint(gname).String(g.iface).Uint(version).NewID(id))
	return id
}

func (h *Host) handleRegistry(op uint16, ev *wl.Event) {
	switch op {
	case 0: // global(name, interface, version)
		gname := ev.Uint()
		iface := ev.String()
		version := ev.Uint()
		h.mu.Lock()
		h.globals[gname] = global{iface, version}
		if iface == "wl_output" {
			o := &output{gname: gname, scale: 1}
			h.outputs[gname] = o
			o.id = h.c.NewObject(func(op uint16, ev *wl.Event) { h.handleOutput(o, op, ev) })
			_ = h.c.Send(h.registry, 0, wl.NewMsg().Uint(gname).String(iface).Uint(min(version, 4)).NewID(o.id))
		}
		h.mu.Unlock()
	case 1: // global_remove(name)
		gname := ev.Uint()
		h.mu.Lock()
		delete(h.globals, gname)
		if o, ok := h.outputs[gname]; ok {
			o.gone = true
			delete(h.outputs, gname)
		}
		h.mu.Unlock()
	}
}

func (h *Host) handleOutput(o *output, op uint16, ev *wl.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch op {
	case 0: // geometry
		o.x, o.y = ev.Int(), ev.Int()
		_, _ = ev.Int(), ev.Int() // physical mm
		_ = ev.Int()              // subpixel
		o.make_, o.model_ = ev.String(), ev.String()
		o.transform = ev.Int()
	case 1: // mode(flags, w, h, refresh)
		flags := ev.Uint()
		w, hh := ev.Int(), ev.Int()
		if flags&1 != 0 { // current
			o.w, o.h = w, hh
		}
	case 3: // scale
		o.scale = ev.Int()
	case 4: // name
		o.name = ev.String()
	case 5: // description
		o.desc = ev.String()
	}
}

func (h *Host) handleSeat(op uint16, ev *wl.Event) {
	if op != 0 { // capabilities
		return
	}
	caps := ev.Uint()
	h.mu.Lock()
	defer h.mu.Unlock()
	if caps&1 != 0 && h.pointer == 0 {
		h.pointer = h.c.NewObject(h.handlePointer)
		_ = h.c.Send(h.seat, 0, wl.NewMsg().NewID(h.pointer))
	}
}

func (h *Host) handlePointer(op uint16, ev *wl.Event) {
	pe := PointerEvent{Time: time.Now()}
	var sid uint32
	switch op {
	case 0: // enter(serial, surface, x, y)
		_ = ev.Uint()
		sid = ev.Uint()
		pe.Kind = PointerEnter
		pe.X, pe.Y = ev.Fixed(), ev.Fixed()
		h.mu.Lock()
		h.ptrFocus = sid
		h.mu.Unlock()
	case 1: // leave(serial, surface)
		_ = ev.Uint()
		sid = ev.Uint()
		pe.Kind = PointerLeave
		h.mu.Lock()
		if h.ptrFocus == sid {
			h.ptrFocus = 0
		}
		h.mu.Unlock()
	case 3: // button(serial, time, button, state)
		_, _ = ev.Uint(), ev.Uint()
		pe.Kind = PointerButton
		pe.Button = ev.Uint()
		pe.Pressed = ev.Uint() == 1
		h.mu.Lock()
		sid = h.ptrFocus
		h.mu.Unlock()
	default:
		return
	}
	h.mu.Lock()
	if s, ok := h.surfaces[sid]; ok {
		pe.Target, pe.Display = "overlay", s.display
	} else if tag, ok := h.tags[sid]; ok {
		pe.Target = tag
	}
	h.mu.Unlock()
	if pe.Target == "" {
		return
	}
	select {
	case h.ptr <- pe:
	default:
	}
}

// PointerEvents delivers pointer enter/leave on overlay surfaces.
func (h *Host) PointerEvents() <-chan PointerEvent { return h.ptr }

// Globals lists the compositor globals, for diagnostics.
func (h *Host) Globals() map[string]uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]uint32{}
	for _, g := range h.globals {
		out[g.iface] = max(out[g.iface], g.version)
	}
	return out
}

// HasFractionalScale reports whether surfaces can use wp_fractional_scale_v1.
func (h *Host) HasFractionalScale() bool { return h.fracMgr != 0 && h.viewporter != 0 }

// Displays lists the outputs known to this connection. Bounds come from
// wl_output.geometry, which compositors fill with the logical position;
// xdg-output would be more precise and is left to the DisplayService.
func (h *Host) Displays() []contracts.Display {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []contracts.Display
	for _, o := range h.outputs {
		w, hh := o.w, o.h
		if o.transform%2 == 1 { // 90 / 270
			w, hh = hh, w
		}
		s := float64(max(o.scale, 1))
		out = append(out, contracts.Display{
			ID:       o.displayID(),
			Name:     o.name,
			Bounds:   contracts.RectXYWH(float64(o.x), float64(o.y), float64(w)/s, float64(hh)/s),
			Native:   contracts.PixelSize{W: int(w), H: int(hh)},
			Scale:    s,
			Rotation: int(o.transform%4) * 90,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (h *Host) outputFor(id contracts.DisplayID) (*output, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, o := range h.outputs {
		if o.displayID() == id {
			return o, true
		}
	}
	return nil, false
}

// Open creates a click-through overlay on display and waits for the
// compositor's first configure.
func (h *Host) Open(ctx context.Context, display contracts.DisplayID) (contracts.OverlaySurface, error) {
	o, ok := h.outputFor(display)
	if !ok {
		return nil, fmt.Errorf("overlay: unknown display %q", display)
	}
	s, err := h.newSurface(o)
	if err != nil {
		return nil, err
	}
	if err := s.waitConfigured(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close destroys all surfaces and the connection.
func (h *Host) Close() error {
	h.mu.Lock()
	ss := make([]*Surface, 0, len(h.surfaces))
	for _, s := range h.surfaces {
		ss = append(ss, s)
	}
	h.mu.Unlock()
	for _, s := range ss {
		s.Close()
	}
	return h.c.Close()
}

func scaleFromOutput(o *output) float64 { return float64(max(o.scale, 1)) }

func roundScaled(v uint32, scale float64) int {
	return int(math.Round(float64(v) * scale))
}

var errClosed = errors.New("overlay: surface closed")
