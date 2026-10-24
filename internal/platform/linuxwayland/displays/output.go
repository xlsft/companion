package displays

import (
	"fmt"
	"math"

	"github.com/xlsft/companion/pkg/contracts"
)

// Opcodes of the interfaces we speak (events unless marked request).
const (
	opDisplayError = 0

	opRegistryBind         = 0 // request
	opRegistryGlobal       = 0
	opRegistryGlobalRemove = 1

	opCallbackDone = 0

	opOutputRelease     = 0 // request (since v3)
	opOutputGeometry    = 0
	opOutputMode        = 1
	opOutputDone        = 2
	opOutputScale       = 3
	opOutputName        = 4
	opOutputDescription = 5

	opXDGManagerDestroy   = 0 // request
	opXDGManagerGetOutput = 1 // request
	opXDGOutputDestroy    = 0 // request
	opXDGLogicalPosition  = 0
	opXDGLogicalSize      = 1
	opXDGDone             = 2 // deprecated since v3, wl_output.done replaces it
	opXDGName             = 3
	opXDGDescription      = 4

	modeCurrent = 0x1
)

// output accumulates the events of one wl_output and its xdg_output. It is
// only touched from the read loop and, under Backend.mu, by Displays.
type output struct {
	global    uint32 // registry name
	id        uint32 // wl_output object id
	version   uint32
	xdgID     uint32
	name      string
	desc      string
	x, y      int32
	transform int32
	modeW     int32
	modeH     int32
	factor    int32 // integer wl_output.scale
	done      bool  // wl_output.done seen at least once

	hasLogical     bool
	lx, ly, lw, lh int32
}

// onOutputEvent applies one wl_output event.
func (o *output) onOutputEvent(op uint16, r *argReader) error {
	switch op {
	case opOutputGeometry:
		o.x, o.y = r.i32(), r.i32()
		r.i32() // physical width mm
		r.i32() // physical height mm
		r.i32() // subpixel
		r.str() // make
		r.str() // model
		o.transform = r.i32()
	case opOutputMode:
		flags := r.u32()
		w, h := r.i32(), r.i32()
		r.i32() // refresh
		if flags&modeCurrent != 0 {
			o.modeW, o.modeH = w, h
		}
	case opOutputDone:
		o.done = true
	case opOutputScale:
		o.factor = r.i32()
	case opOutputName:
		o.name = r.str()
	case opOutputDescription:
		o.desc = r.str()
	}
	return r.err
}

// onXDGEvent applies one zxdg_output_v1 event.
func (o *output) onXDGEvent(op uint16, r *argReader) error {
	switch op {
	case opXDGLogicalPosition:
		o.lx, o.ly = r.i32(), r.i32()
	case opXDGLogicalSize:
		o.lw, o.lh = r.i32(), r.i32()
		o.hasLogical = true
	case opXDGName:
		if o.name == "" {
			o.name = r.str()
		}
	}
	return r.err
}

// ready reports whether the output has enough data to be listed. With the
// xdg-output manager present we wait for the logical size too, otherwise a
// hotplugged output would briefly show up with fallback geometry.
func (o *output) ready(haveXDG bool) bool {
	return o.done && o.modeW > 0 && o.modeH > 0 && (o.hasLogical || !haveXDG)
}

// rotation maps wl_output.transform to clockwise degrees as
// contracts.Display expects. Wayland transforms rotate counter-clockwise
// ("90" = 90° CCW), so 90 and 270 swap. Flipped variants (4-7) carry the
// same rotation in their low two bits; the mirroring itself is ignored.
func rotation(transform int32) int {
	return (360 - int(transform&3)*90) % 360
}

// display converts the accumulated state. Native is reported in the
// orientation the screen is shown in (swapped for 90/270), so that
// Scale = Native / Bounds holds for every rotation.
func (o *output) display() contracts.Display {
	rot := rotation(o.transform)
	nw, nh := int(o.modeW), int(o.modeH)
	if rot == 90 || rot == 270 {
		nw, nh = nh, nw
	}
	bx, by := float64(o.x), float64(o.y)
	var lw, lh float64
	factor := float64(max(o.factor, 1))
	if o.hasLogical && o.lw > 0 && o.lh > 0 {
		bx, by = float64(o.lx), float64(o.ly)
		lw, lh = float64(o.lw), float64(o.lh)
	} else {
		lw, lh = float64(nw)/factor, float64(nh)/factor
	}
	id := o.name
	if id == "" {
		id = fmt.Sprintf("output-%d", o.global)
	}
	name := o.desc
	if name == "" {
		name = id
	}
	return contracts.Display{
		ID:       contracts.DisplayID(id),
		Name:     name,
		Bounds:   contracts.RectXYWH(bx, by, lw, lh),
		Native:   contracts.PixelSize{W: nw, H: nh},
		Scale:    scaleOf(nw, lw, factor),
		Rotation: rot,
	}
}

// scaleOf derives native/logical. Compositors round the logical size to
// whole pixels (1.5x on 2560 gives 1707), so snap to the 1/120 grid that
// wp-fractional-scale uses and the raw ratio's noise disappears.
func scaleOf(native int, logical, fallback float64) float64 {
	if logical <= 0 || native <= 0 {
		return fallback
	}
	s := math.Round(float64(native)/logical*120) / 120
	return math.Round(s*10000) / 10000
}
