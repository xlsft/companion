package overlay

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"time"

	"golang.org/x/image/vector"

	"github.com/xlsft/companion/pkg/contracts"
)

// Motion: every hint pops in and then carries a light glint that keeps
// travelling, so it is noticed on a busy screen without flashing.
const (
	appearDur   = 350 * time.Millisecond
	glintPeriod = 1800 * time.Millisecond
	frameEvery  = 16 * time.Millisecond // ~60 fps while anything is shown
)

// anim is a shape's animation state for one frame.
type anim struct {
	appear float64 // 0 → 1 over appearDur, eased
	phase  float64 // 0 → 1 repeating every glintPeriod
	live   bool    // draw the glint; false renders the settled look
}

// settled is the final look without motion (reduced motion, tests).
var settled = anim{appear: 1}

func animAt(born, now time.Time) anim {
	age := now.Sub(born)
	a := min(1, max(0, float64(age)/float64(appearDur)))
	// Ease out cubic: fast start, gentle landing.
	a = 1 - math.Pow(1-a, 3)
	return anim{
		appear: a,
		phase:  math.Mod(float64(age)/float64(glintPeriod), 1),
		live:   true,
	}
}

// fade scales c's opacity by the entrance progress.
func (an anim) fade(c color.NRGBA) color.NRGBA { return alpha(c, an.appear) }

// grow is the entrance overshoot: shapes start this many times larger and
// settle to 1.
func (an anim) grow(amount float64) float64 { return 1 + amount*(1-an.appear) }

var white = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}

func mix(a, b color.NRGBA, f float64) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x)*(1-f) + float64(y)*f + 0.5) }
	return color.NRGBA{R: m(a.R, b.R), G: m(a.G, b.G), B: m(a.B, b.B), A: m(a.A, b.A)}
}

// glintLayers are the glint's soft core: shorter, brighter layers on top.
var glintLayers = []struct{ span, light, alpha float64 }{
	{0.16, 0.30, 0.55},
	{0.10, 0.55, 0.75},
	{0.05, 0.85, 1.00},
}

// arcGlint draws the glint on a circle stroke at angle phase.
func arcGlint(p *painter, c contracts.Point, r, w float64, an anim, col color.NRGBA) {
	if !an.live {
		return
	}
	mid := an.phase*2*math.Pi - math.Pi/2
	for _, l := range glintLayers {
		half := l.span * math.Pi
		p.color(alpha(mix(col, white, l.light), l.alpha*an.appear))
		arcBand(p, c, r, w, mid-half, mid+half)
		p.fill()
	}
}

// pathGlint draws the glint travelling along a polyline. On a closed path
// it loops around; on an open one it slides out past the end while fading
// and fades back in at the start, instead of jumping back.
func pathGlint(p *painter, pts []contracts.Point, closed bool, w float64, an anim, col color.NRGBA) {
	if !an.live || len(pts) < 2 {
		return
	}
	pos, env := an.phase, 1.0
	if !closed {
		pos, env = openGlint(an.phase)
	}
	for _, l := range glintLayers {
		from, to := pos-l.span/2, pos+l.span/2
		p.color(alpha(mix(col, white, l.light), l.alpha*an.appear*env))
		for _, sub := range subpaths(pts, closed, from, to) {
			polyline(p, sub, w)
		}
	}
}

// glintMargin is how far past either end of an open path the glint
// travels, so it leaves and enters gradually.
const glintMargin = 0.12

// openGlint maps the repeating phase to a position on an open path, from
// before its start to past its end, and an opacity envelope that is 0 at
// both extremes.
func openGlint(phase float64) (pos, env float64) {
	pos = -glintMargin + phase*(1+2*glintMargin)
	edge := min(pos+glintMargin, 1+glintMargin-pos) / (2 * glintMargin)
	env = min(1, max(0, edge))
	env = env * env * (3 - 2*env) // smoothstep
	return pos, env
}

// subpaths returns the part of the path between fractions from and to of
// its length; on a closed path a range past either end wraps around.
func subpaths(pts []contracts.Point, closed bool, from, to float64) [][]contracts.Point {
	if closed {
		pts = append(pts[:len(pts):len(pts)], pts[0])
		if from < 0 {
			return append(subpaths(pts, false, from+1, 1), subpaths(pts, false, 0, to)...)
		}
		if to > 1 {
			return append(subpaths(pts, false, from, 1), subpaths(pts, false, 0, to-1)...)
		}
	}
	from, to = max(0, from), min(1, to)
	if from >= to {
		return nil
	}
	total := 0.0
	for i := 1; i < len(pts); i++ {
		total += dist(pts[i-1], pts[i])
	}
	a, b := from*total, to*total
	var out []contracts.Point
	acc := 0.0
	for i := 1; i < len(pts); i++ {
		l := dist(pts[i-1], pts[i])
		s0, s1 := acc, acc+l
		acc = s1
		if s1 < a || s0 > b || l == 0 {
			continue
		}
		lerp := func(s float64) contracts.Point {
			t := (s - s0) / l
			return contracts.Point{X: pts[i-1].X + (pts[i].X-pts[i-1].X)*t, Y: pts[i-1].Y + (pts[i].Y-pts[i-1].Y)*t}
		}
		if len(out) == 0 {
			out = append(out, lerp(max(a, s0)))
		}
		out = append(out, lerp(min(b, s1)))
	}
	if len(out) < 2 {
		return nil
	}
	return [][]contracts.Point{out}
}

func dist(a, b contracts.Point) float64 { return math.Hypot(b.X-a.X, b.Y-a.Y) }

// sweep draws a slanted light band moving across box, visible only inside
// the filled polygons mask (a label plate, a badge, the cursor glyph): the
// classic shimmer of a loading placeholder.
func sweep(dst *canvas, box image.Rectangle, mask [][]contracts.Point, an anim, strength float64) {
	box = box.Intersect(dst.Bounds())
	if !an.live || box.Empty() {
		return
	}
	dst.mark(box)
	m := image.NewAlpha(box)
	mp := &painter{box: box, r: &vector.Rasterizer{}, mask: m, masking: true}
	for _, poly := range mask {
		mp.polygon(poly)
		mp.fill()
	}
	bw := float64(box.Dx())
	band := max(14, bw*0.22)
	slant := float64(box.Dy()) * 0.6
	x := float64(box.Min.X) - band - slant + an.phase*(bw+2*band+2*slant)
	y0, y1 := float64(box.Min.Y), float64(box.Max.Y)
	light := image.NewRGBA(box)
	lp := &painter{dst: light, box: box, r: &vector.Rasterizer{}, src: image.NewUniform(alpha(white, strength*an.appear))}
	lp.polygon([]contracts.Point{{X: x + slant, Y: y0}, {X: x + slant + band, Y: y0}, {X: x + band, Y: y1}, {X: x, Y: y1}})
	lp.fill()
	draw.DrawMask(dst.RGBA, box, light, box.Min, m, box.Min, draw.Over)
}
