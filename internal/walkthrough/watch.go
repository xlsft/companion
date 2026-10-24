package walkthrough

import (
	"image"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Auto-advance: while a step is shown the screen is sampled as tiny
// grayscale thumbnails and compared with the step's baseline. Only a
// change that is large and has settled asks the model whether the step is
// done, and those checks are spaced and capped, so a busy screen costs a
// few captures, never a stream of model requests.
const (
	// WatchInterval is how often the screen is sampled.
	WatchInterval = 1500 * time.Millisecond
	// thumbSide bounds the longer side of a thumbnail: a cell then covers
	// a few logical pixels of a step's area and ~30 of a whole display,
	// enough to see a menu open but blind to a cursor blinking.
	thumbSide = 80
	// thumbSamples per cell side: a cell's brightness is the mean of
	// thumbSamples² pixels rather than all of them, which keeps a sample
	// cheap even on a 4K capture.
	thumbSamples = 4
	// pixelDelta is how far (of 255) a cell's brightness must move to
	// count as changed; it ignores compression-like noise and subpixel
	// text rendering.
	pixelDelta = 24
	// changeFrac is the share of changed cells that makes a frame
	// different. The hint's own glint touches well under it.
	changeFrac = 0.03
	// stableSamples changed frames in a row, each like the one before, are
	// a settled change; a video or an animation never settles.
	stableSamples = 2
	// minCheckGap spaces the model checks of one step.
	minCheckGap = 5 * time.Second
	// maxChecks per step; after that only the user moves on.
	maxChecks = 4
	// maxWatchErrors capture failures in a row stop watching the step.
	maxWatchErrors = 3
	// areaMargin grows a step's hint area so the control the user acts on
	// and what it opens next to it are both inside; minimum in logical
	// pixels, else a share of the area's longer side.
	areaMargin      = 64
	areaMarginShare = 0.5
)

// Thumb is a tiny grayscale picture of the screen. Its pixels are screen
// content: Zero them once compared.
type Thumb struct {
	W, H int
	Pix  []uint8
}

// From fills t with img scaled to fit side×side, reusing t's buffer.
func (t *Thumb) From(img *image.RGBA, side int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		t.W, t.H, t.Pix = 0, 0, t.Pix[:0]
		return
	}
	tw, th := w, h
	if w > side || h > side {
		if w >= h {
			tw, th = side, max(1, h*side/w)
		} else {
			tw, th = max(1, w*side/h), side
		}
	}
	t.W, t.H = tw, th
	if cap(t.Pix) < tw*th {
		t.Pix = make([]uint8, tw*th)
	}
	t.Pix = t.Pix[:tw*th]
	for cy := 0; cy < th; cy++ {
		y0, y1 := cy*h/th, max((cy+1)*h/th, cy*h/th+1)
		for cx := 0; cx < tw; cx++ {
			x0, x1 := cx*w/tw, max((cx+1)*w/tw, cx*w/tw+1)
			sum, n := 0, 0
			for sy := 0; sy < thumbSamples; sy++ {
				y := y0 + (y1-y0)*(2*sy+1)/(2*thumbSamples)
				row := img.Pix[(y)*img.Stride:]
				for sx := 0; sx < thumbSamples; sx++ {
					x := x0 + (x1-x0)*(2*sx+1)/(2*thumbSamples)
					p := row[4*x : 4*x+3 : 4*x+3]
					sum += (77*int(p[0]) + 150*int(p[1]) + 29*int(p[2])) >> 8
					n++
				}
			}
			t.Pix[cy*tw+cx] = uint8(sum / n)
		}
	}
}

// copyFrom makes t a copy of o.
func (t *Thumb) copyFrom(o *Thumb) {
	t.W, t.H = o.W, o.H
	t.Pix = append(t.Pix[:0], o.Pix...)
}

// Zero wipes the pixels.
func (t *Thumb) Zero() { clear(t.Pix[:cap(t.Pix)]) }

// changed is the share of cells whose brightness differs by more than
// pixelDelta; thumbnails of different sizes differ entirely.
func changed(a, b *Thumb) float64 {
	if a.W != b.W || a.H != b.H || len(a.Pix) == 0 {
		return 1
	}
	n := 0
	for i, p := range a.Pix {
		d := int(p) - int(b.Pix[i])
		if d > pixelDelta || d < -pixelDelta {
			n++
		}
	}
	return float64(n) / float64(len(a.Pix))
}

// verdict is what one sample means.
type verdict int

const (
	keepWatching verdict = iota
	// askModel: the screen changed and settled; ask whether the step is done.
	askModel
	// giveUp: the step's checks are used up.
	giveUp
)

// detector decides from thumbnails when to ask the model; it holds the
// step's baseline and the previous sample.
type detector struct {
	base, prev Thumb
	hasBase    bool
	streak     int // settled changed samples in a row
	checks     int
	last       time.Time
}

func (d *detector) observe(t *Thumb, now time.Time) verdict {
	if !d.hasBase || d.base.W != t.W || d.base.H != t.H {
		// First look, or the area moved to another display.
		d.base.copyFrom(t)
		d.prev.copyFrom(t)
		d.hasBase, d.streak = true, 0
		return keepWatching
	}
	if changed(t, &d.base) < changeFrac {
		d.streak = 0
		d.prev.copyFrom(t)
		return keepWatching
	}
	if d.streak > 0 && changed(t, &d.prev) < changeFrac {
		d.streak++
	} else {
		d.streak = 1
	}
	d.prev.copyFrom(t)
	if d.streak < stableSamples {
		return keepWatching
	}
	if d.checks >= maxChecks {
		return giveUp
	}
	if !d.last.IsZero() && now.Sub(d.last) < minCheckGap {
		// Settled again too soon: ask once the gap has passed.
		return keepWatching
	}
	d.checks++
	d.last = now
	// What the model has seen is the new baseline: only a further change
	// asks again.
	d.base.copyFrom(t)
	d.streak = 0
	return askModel
}

// rebase forgets the baseline, e.g. because the hints moved.
func (d *detector) rebase() {
	d.base.Zero()
	d.prev.Zero()
	d.hasBase, d.streak = false, 0
}

func (d *detector) close() { d.rebase() }

// watchArea is the desktop area sampled for a step marked at area: grown
// by areaMargin, or zero (the whole display) when the step has no hints.
// A point hint has an area of zero size, which still counts.
func watchArea(area contracts.Rect) contracts.Rect {
	if area == (contracts.Rect{}) || area.Min.X > area.Max.X || area.Min.Y > area.Max.Y {
		return contracts.Rect{}
	}
	m := max(areaMargin, areaMarginShare*max(area.Width(), area.Height()))
	return contracts.Rect{
		Min: contracts.Point{X: area.Min.X - m, Y: area.Min.Y - m},
		Max: contracts.Point{X: area.Max.X + m, Y: area.Max.Y + m},
	}
}
