package overlay

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/xlsft/companion/internal/assets/fonts"
	"github.com/xlsft/companion/pkg/contracts"
)

// Look of the hints, from the design prototypes. Sizes are logical pixels
// and are multiplied by the display scale.
var (
	defaultColor = color.NRGBA{R: 0x8B, G: 0x5C, B: 0xF6, A: 0xFF} // brand violet, as the logo
	labelBg      = color.NRGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xEB}
	labelFg      = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
)

const (
	defaultStroke = 4.0
	rectStroke    = 3.0
	// glowSpread is how far the soft glow reaches beyond a stroke and
	// glowAlpha its opacity next to the stroke.
	glowSpread = 10.0
	glowAlpha  = 0.45
	cornerR    = 6.0
	dimAlpha   = 0.28 // scrim around an emphasised rect
	dashLen    = 7.0
	dashGap    = 5.0
)

// shape is an annotation resolved to surface (native pixel) coordinates of
// one display.
type shape struct {
	kind   Kind
	pts    []contracts.Point
	radius float64 // native pixels
	text   string
	style  Style
	scale  float64   // native pixels per logical pixel of the display
	born   time.Time // when it appeared, for the entrance animation
	cache  *shapeCache
}

// render draws shapes onto a transparent frame of the given native size.
func render(size contracts.PixelSize, shapes []shape) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, size.W, size.H))
	renderInto(frame, shapes, time.Time{}, false, image.Rectangle{})
	return frame
}

// layer selects which parts of a shape drawShape paints: the static look
// (stroke, glow, fill), the live parts that change every frame (glint,
// breathing, marching dashes) or both.
type layer uint8

const (
	layerStatic layer = 1 << iota
	layerLive
	layerAll = layerStatic | layerLive
)

func (l layer) static() bool { return l&layerStatic != 0 }
func (l layer) live() bool   { return l&layerLive != 0 }

// shapeCache holds a shape's settled static layer, rendered once and then
// blitted every frame: glows are many translucent layers and redrawing
// them at 60 fps for every hint would not fit the frame budget.
type shapeCache struct {
	img *image.RGBA // bounds in frame coordinates
}

// cacheable kinds have a static layer worth caching and a bounded box.
func cacheable(k Kind) bool {
	switch k {
	case KindRing, KindCircle, KindRect, KindHighlight, KindArrow, KindUnderline, KindFreehand, KindBadge:
		return true
	}
	return false
}

// dims reports shapes that darken the screen; they are drawn first.
func dims(s shape) bool {
	return s.kind == KindSpotlight || s.kind == KindRect && s.style.Emphasis
}

// renderInto clears the dirty area of frame (what the previous frame drew;
// the rest is already transparent) and draws shapes as they look at now.
// It returns the area drawn and, within it, the part that may differ from
// the previous frame of the same scene. With motion off shapes are drawn
// settled.
func renderInto(frame *image.RGBA, shapes []shape, now time.Time, motion bool, dirty image.Rectangle) (drawn, volatile image.Rectangle) {
	dirty = dirty.Intersect(frame.Bounds())
	for y := dirty.Min.Y; y < dirty.Max.Y; y++ {
		i := frame.PixOffset(dirty.Min.X, y)
		clear(frame.Pix[i : i+4*dirty.Dx()])
	}
	cv := &canvas{RGBA: frame}
	// Scrims go first so they can be filled with plain copies onto the
	// cleared frame and still sit under every hint.
	for _, pass := range []bool{true, false} {
		for _, s := range shapes {
			if dims(s) != pass {
				continue
			}
			an := settled
			if motion {
				an = animAt(s.born, now)
			}
			cv.settled = an.appear >= 1
			if s.cache != nil && an.appear >= 1 && cacheable(s.kind) && !dims(s) {
				if s.cache.img == nil {
					s.cache.img = renderStatic(frame.Bounds(), s, an)
				}
				r := s.cache.img.Bounds()
				draw.Draw(frame, r, s.cache.img, r.Min, draw.Over)
				cv.drawn = cv.drawn.Union(r)
				drawShape(cv, s, an, layerLive)
				continue
			}
			drawShape(cv, s, an, layerAll)
		}
	}
	return cv.drawn, cv.volatile
}

// renderStatic draws the static layer of s into an image just big enough.
func renderStatic(frame image.Rectangle, s shape, an anim) *image.RGBA {
	box := shapeBox(s).Intersect(frame)
	cv := &canvas{RGBA: image.NewRGBA(box), settled: true}
	drawShape(cv, s, an, layerStatic)
	return cv.RGBA.SubImage(cv.drawn.Intersect(box)).(*image.RGBA)
}

// shapeBox generously bounds everything a cacheable shape can draw.
func shapeBox(s shape) image.Rectangle {
	k := s.scale
	pad := s.radius + (defaultStroke+glowSpread+badgeRadius+8)*k + max(s.style.StrokeWidth*k, 0)
	return bounds(s.pts).Inset(-int(pad + 2))
}

func drawShape(dst *canvas, s shape, an anim, l layer) {
	c := parseColor(s.style.Color)
	k := s.scale
	w := s.style.StrokeWidth
	if w <= 0 {
		w = defaultStroke
		switch s.kind {
		case KindRect, KindBrackets:
			w = rectStroke
		case KindSpotlight:
			w = 2
		}
	}
	w *= k
	switch s.kind {
	case KindPoint:
		cursor(dst, s.pts[0], k, c, an)
	case KindRing:
		ctr, r := s.pts[0], s.radius*an.grow(0.35)
		p := newPainter(dst, around(ctr, r+w/2+glowSpread*k))
		if l.static() {
			if s.style.Emphasis {
				// Like CSS box-shadow, the glow spreads outwards only.
				glow(p, an.fade(c), glowSpread*k, func(extra float64) { annulus(p, ctr, r+extra/2, w+extra) })
			}
			p.color(an.fade(c))
			annulus(p, ctr, r, w)
			p.fill()
		}
		if l.live() {
			arcGlint(p, ctr, r, w, an, c)
		}
	case KindCircle:
		ctr, r := s.pts[0], s.radius*an.grow(0.35)
		p := newPainter(dst, around(ctr, r+w))
		p.color(an.fade(c))
		switch {
		case s.style.Dashed && l.live():
			// Marching dashes: the pattern rotates with the glint phase.
			dashedCircle(p, ctr, r, w/2, dashLen*k, dashGap*k, an.phase*2*math.Pi*0.25)
			arcGlint(p, ctr, r, w/2, an, c)
		case s.style.Dashed:
		default:
			if l.static() {
				annulus(p, ctr, r, w)
				p.fill()
			}
			if l.live() {
				arcGlint(p, ctr, r, w, an, c)
			}
		}
	case KindSpotlight:
		ctr, r := s.pts[0], s.radius*an.grow(0.6)
		spotlight(dst, ctr, r, 14*k, an)
		p := newPainter(dst, around(ctr, r+w))
		p.color(alpha(an.fade(c), 0.7))
		annulus(p, ctr, r, w)
		p.fill()
		arcGlint(p, ctr, r, w*1.5, an, c)
	case KindRect, KindHighlight, KindBrackets:
		a, b := s.pts[0], s.pts[1]
		r := image.Rect(int(a.X), int(a.Y), int(b.X), int(b.Y)).Canon().Inset(-int(10 * k * (1 - an.appear)))
		switch s.kind {
		case KindRect:
			if s.style.Emphasis && l.static() {
				dimOutside(dst, r, cornerR*k, an.appear)
			}
			p := newPainter(dst, r.Inset(-int(w+glowSpread*k+1)))
			if l.static() {
				glow(p, an.fade(c), glowSpread*k, func(extra float64) { roundRectBand(p, r, cornerR*k, -w/2, w/2+extra) })
				p.color(an.fade(c))
				roundRectStroke(p, r, cornerR*k, w)
				p.fill()
			}
			if l.live() {
				pathGlint(p, roundRect(r, cornerR*k, 0, false), true, w, an, c)
			}
		case KindHighlight:
			plate := roundRect(r, 3*k, 2*k, false)
			if l.static() {
				p := newPainter(dst, r.Inset(-int(3*k+1)))
				p.color(alpha(an.fade(c), 0.28))
				p.polygon(plate)
				p.fill()
			}
			if l.live() {
				sweep(dst, r.Inset(-int(3*k+1)), [][]contracts.Point{plate}, an, 0.35)
			}
		case KindBrackets:
			// The corners breathe in and out a few pixels.
			breath := 0.0
			if an.live {
				breath = 3 * k * (0.5 - 0.5*math.Cos(an.phase*2*math.Pi))
			}
			rb := r.Inset(-int(breath))
			p := newPainter(dst, rb.Inset(-int(w+glowSpread*k+1)))
			arms := brackets(rb, min(22*k, float64(rb.Dx())/3, float64(rb.Dy())/3))
			glow(p, an.fade(c), glowSpread*k*0.6, func(extra float64) {
				for _, arm := range arms {
					polyline(p, arm, w+extra)
				}
			})
			p.color(an.fade(mix(c, white, 0.25*breath/(3*k+1e-9))))
			for _, arm := range arms {
				polyline(p, arm, w)
			}
		}
	case KindArrow:
		a, b := s.pts[0], s.pts[1]
		// Entrance: the arrow is drawn out from its tail.
		b = contracts.Point{X: a.X + (b.X-a.X)*an.appear, Y: a.Y + (b.Y-a.Y)*an.appear}
		box := image.Rect(int(a.X), int(a.Y), int(s.pts[1].X), int(s.pts[1].Y)).Canon().Inset(-int(6*w + 2))
		p := newPainter(dst, box)
		p.color(c)
		base := shaftEnd(a, b, w)
		if l.static() {
			arrow(p, a, b, w)
		}
		if l.live() {
			// The glint runs the whole length, tip included: past the
			// shaft it lights the head in proportion to its overlap.
			// The stroke stops inside the head so its round cap cannot
			// poke out past the tip.
			inHead := contracts.Point{X: base.X + (b.X-base.X)*0.7, Y: base.Y + (b.Y-base.Y)*0.7}
			pathGlint(p, []contracts.Point{a, base, inHead}, false, w, an, c)
			headGlint(p, a, base, b, w, an, c)
		}
	case KindUnderline, KindFreehand:
		pts := s.pts
		if an.appear < 1 {
			if sub := subpaths(pts, false, 0, an.appear); len(sub) > 0 {
				pts = sub[0]
			} else {
				return
			}
		}
		p := newPainter(dst, bounds(s.pts).Inset(-int(w+2)))
		p.color(c)
		if l.static() {
			polyline(p, pts, w)
		}
		if l.live() {
			pathGlint(p, pts, false, w, an, c)
		}
	case KindBadge:
		badge(dst, s.pts[0], s.text, k, c, an, l)
	case KindLabel, KindImageCard:
		// Image cards show the referenced image in the panel UI; on the
		// overlay they are a label with the reference name.
		label(dst, s.pts[0], s.text, k, an)
	}
}

// canvas is a frame being drawn; it records which pixels were touched so
// the next frame only clears, and the surface only uploads, that area.
type canvas struct {
	*image.RGBA
	drawn, volatile image.Rectangle
	// settled is set while drawing a shape that has finished its
	// entrance; scrims it draws then look the same every frame.
	settled bool
}

// mark records r as drawn and as possibly changed since the last frame.
func (c *canvas) mark(r image.Rectangle) {
	r = r.Intersect(c.Bounds())
	c.drawn = c.drawn.Union(r)
	c.volatile = c.volatile.Union(r)
}

// markStable records r as drawn; once the shape has settled its pixels
// there are the same as in the previous frame.
func (c *canvas) markStable(r image.Rectangle) {
	if !c.settled {
		c.mark(r)
		return
	}
	c.drawn = c.drawn.Union(r.Intersect(c.Bounds()))
}

// painter accumulates polygons and composites them with fill. Each fill
// rasterizes only the bounds of the polygons added since the previous
// fill, clipped to the painter's box: shapes are built from many small
// pieces (segments, joints, glow layers), and rasterizing the whole shape
// box per piece cost hundreds of milliseconds per frame.
//
// The rasterizer takes |sum| of signed coverage, so paths that must cancel
// (ring holes) share one fill, while independent pieces get their own fill
// to avoid opposite windings cancelling each other. Between beginStroke
// and endStroke fills go into a coverage mask instead, which is then
// composited once, so a stroke built from overlapping pieces blends as one
// shape even with a translucent colour.
type painter struct {
	dst   *image.RGBA
	box   image.Rectangle
	r     *vector.Rasterizer
	src   *image.Uniform
	polys [][]contracts.Point

	mask    *image.Alpha
	masking bool
	maskB   image.Rectangle
}

func newPainter(dst *canvas, box image.Rectangle) *painter {
	box = box.Intersect(dst.Bounds())
	dst.mark(box)
	return &painter{dst: dst.RGBA, box: box, src: image.NewUniform(color.NRGBA{}), r: &vector.Rasterizer{}}
}

// newStablePainter is newPainter for content that does not change once
// its shape has settled (scrims).
func newStablePainter(dst *canvas, box image.Rectangle) *painter {
	box = box.Intersect(dst.Bounds())
	dst.markStable(box)
	return &painter{dst: dst.RGBA, box: box, src: image.NewUniform(color.NRGBA{}), r: &vector.Rasterizer{}}
}

func (p *painter) color(c color.NRGBA) { p.src.C = c }

func (p *painter) polygon(pts []contracts.Point) {
	if len(pts) >= 3 && !p.box.Empty() {
		p.polys = append(p.polys, pts)
	}
}

// pending is the pixel bounds of the polygons waiting for fill.
func (p *painter) pending() image.Rectangle {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, poly := range p.polys {
		for _, q := range poly {
			x0, y0 = min(x0, q.X), min(y0, q.Y)
			x1, y1 = max(x1, q.X), max(y1, q.Y)
		}
	}
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1))+1, int(math.Ceil(y1))+1).Intersect(p.box)
}

func (p *painter) fill() {
	if len(p.polys) == 0 {
		return
	}
	b := p.pending()
	if !b.Empty() {
		p.r.Reset(b.Dx(), b.Dy())
		ox, oy := float64(b.Min.X), float64(b.Min.Y)
		for _, poly := range p.polys {
			p.r.MoveTo(float32(poly[0].X-ox), float32(poly[0].Y-oy))
			for _, q := range poly[1:] {
				p.r.LineTo(float32(q.X-ox), float32(q.Y-oy))
			}
			p.r.ClosePath()
		}
		if p.masking {
			p.r.Draw(p.mask, b, image.Opaque, image.Point{})
			p.maskB = p.maskB.Union(b)
		} else {
			p.r.Draw(p.dst, b, p.src, image.Point{})
		}
	}
	p.polys = p.polys[:0]
}

func (p *painter) beginStroke() {
	if p.mask == nil {
		p.mask = image.NewAlpha(p.box)
	}
	p.masking, p.maskB = true, image.Rectangle{}
}

func (p *painter) endStroke() {
	p.masking = false
	b := p.maskB
	if b.Empty() {
		return
	}
	draw.DrawMask(p.dst, b, p.src, image.Point{}, p.mask, b.Min, draw.Over)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		i := p.mask.PixOffset(b.Min.X, y)
		clear(p.mask.Pix[i : i+b.Dx()])
	}
}

// glow approximates a CSS box-shadow blur: the shape drawn ever wider in
// faint layers, so opacity falls off with distance from the stroke.
func glow(p *painter, c color.NRGBA, spread float64, shape func(extra float64)) {
	const layers = 8
	// Per-layer opacity such that all layers stacked reach glowAlpha.
	a := 1 - math.Pow(1-glowAlpha, 1.0/layers)
	p.color(alpha(c, a))
	for i := layers; i >= 1; i-- {
		shape(spread * float64(i) / layers)
		p.fill()
	}
}

func alpha(c color.NRGBA, a float64) color.NRGBA {
	c.A = uint8(float64(c.A)*a + 0.5)
	return c
}

func around(c contracts.Point, r float64) image.Rectangle {
	return image.Rect(int(c.X-r)-1, int(c.Y-r)-1, int(c.X+r)+2, int(c.Y+r)+2)
}

func bounds(pts []contracts.Point) image.Rectangle {
	r := image.Rect(int(pts[0].X), int(pts[0].Y), int(pts[0].X)+1, int(pts[0].Y)+1)
	for _, q := range pts[1:] {
		r = r.Union(image.Rect(int(q.X), int(q.Y), int(q.X)+1, int(q.Y)+1))
	}
	return r
}

func circlePoints(c contracts.Point, r float64, reverse bool) []contracts.Point {
	n := max(24, int(r/2))
	pts := make([]contracts.Point, n)
	for i := range n {
		a := 2 * math.Pi * float64(i) / float64(n)
		if reverse {
			a = -a
		}
		pts[i] = contracts.Point{X: c.X + r*math.Cos(a), Y: c.Y + r*math.Sin(a)}
	}
	return pts
}

// annulus adds a ring as outer circle plus reversed inner circle; the
// rasterizer's signed coverage cancels the inside.
func annulus(p *painter, c contracts.Point, r, w float64) {
	p.polygon(circlePoints(c, r+w/2, false))
	if inner := r - w/2; inner > 0 {
		p.polygon(circlePoints(c, inner, true))
	}
}

// dashedCircle strokes a circle in dashes of about dash px with gap px
// between them, adjusted so the pattern closes evenly.
func dashedCircle(p *painter, c contracts.Point, r, w, dash, gap, offset float64) {
	circ := 2 * math.Pi * r
	n := max(4, int(math.Round(circ/(dash+gap))))
	step := 2 * math.Pi / float64(n)
	on := step * dash / (dash + gap)
	for i := range n {
		a0 := offset + float64(i)*step
		arcBand(p, c, r, w, a0, a0+on)
		p.fill()
	}
}

// arcBand adds the annular sector between angles a0 and a1.
func arcBand(p *painter, c contracts.Point, r, w, a0, a1 float64) {
	const n = 8
	pts := make([]contracts.Point, 0, 2*(n+1))
	for i := 0; i <= n; i++ {
		a := a0 + (a1-a0)*float64(i)/n
		pts = append(pts, contracts.Point{X: c.X + (r+w/2)*math.Cos(a), Y: c.Y + (r+w/2)*math.Sin(a)})
	}
	for i := n; i >= 0; i-- {
		a := a0 + (a1-a0)*float64(i)/n
		pts = append(pts, contracts.Point{X: c.X + (r-w/2)*math.Cos(a), Y: c.Y + (r-w/2)*math.Sin(a)})
	}
	p.polygon(pts)
}

func disc(p *painter, c contracts.Point, r float64) {
	p.polygon(circlePoints(c, r, false))
}

// roundRect returns the outline of r with corner radius rad, grown by out
// on every side (negative shrinks); reverse flips the winding.
func roundRect(r image.Rectangle, rad, out float64, reverse bool) []contracts.Point {
	x0, y0 := float64(r.Min.X)-out, float64(r.Min.Y)-out
	x1, y1 := float64(r.Max.X)+out, float64(r.Max.Y)+out
	rad = max(0, min(rad+out, (x1-x0)/2, (y1-y0)/2))
	corners := []struct{ cx, cy, a float64 }{
		{x1 - rad, y0 + rad, -math.Pi / 2},
		{x1 - rad, y1 - rad, 0},
		{x0 + rad, y1 - rad, math.Pi / 2},
		{x0 + rad, y0 + rad, math.Pi},
	}
	const n = 6
	pts := make([]contracts.Point, 0, 4*(n+1))
	for _, c := range corners {
		for i := 0; i <= n; i++ {
			a := c.a + math.Pi/2*float64(i)/n
			pts = append(pts, contracts.Point{X: c.cx + rad*math.Cos(a), Y: c.cy + rad*math.Sin(a)})
		}
	}
	if reverse {
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
	}
	return pts
}

func roundRectStroke(p *painter, r image.Rectangle, rad, w float64) {
	roundRectBand(p, r, rad, -w/2, w/2)
}

// roundRectBand adds the band between r grown by in and by out.
func roundRectBand(p *painter, r image.Rectangle, rad, in, out float64) {
	p.polygon(roundRect(r, rad, out, false))
	p.polygon(roundRect(r, rad, in, true))
}

// dimOutside darkens the whole display except r, to focus attention on a
// region during walkthroughs. Only the band around r is rasterized; the
// rest is plain fills, which keeps a full-screen scrim cheap per frame.
func dimOutside(dst *canvas, r image.Rectangle, rad, strength float64) {
	scrim := alpha(color.NRGBA{A: 0xFF}, dimAlpha*strength)
	inner := r.Inset(-2)
	fillOutside(dst, inner, scrim)
	p := newStablePainter(dst, inner)
	p.color(scrim)
	p.polygon(rectPoints(inner))
	p.polygon(roundRect(r, rad, 0, true))
	p.fill()
}

// spotlight darkens the display except a circle with a feathered edge.
func spotlight(dst *canvas, c contracts.Point, r, feather float64, an anim) {
	const layers = 6
	box := around(c, r+feather)
	total := dimAlpha * 1.6 * an.appear
	fillOutside(dst, box, alpha(color.NRGBA{A: 0xFF}, total))
	// Stacked scrims with growing holes fade from clear to the full dim.
	a := 1 - math.Pow(1-total, 1.0/layers)
	p := newStablePainter(dst, box)
	p.color(alpha(color.NRGBA{A: 0xFF}, a))
	for i := range layers {
		hole := r - feather/2 + feather*float64(i)/(layers-1)
		p.polygon(rectPoints(p.box))
		p.polygon(circlePoints(c, hole, true))
		p.fill()
	}
}

// fillOutside composites col over everything outside r.
func fillOutside(dst *canvas, r image.Rectangle, col color.NRGBA) {
	b := dst.Bounds()
	r = r.Intersect(b)
	u := image.NewUniform(col)
	for _, part := range []image.Rectangle{
		{Min: b.Min, Max: image.Pt(b.Max.X, r.Min.Y)},
		{Min: image.Pt(b.Min.X, r.Max.Y), Max: b.Max},
		{Min: image.Pt(b.Min.X, r.Min.Y), Max: image.Pt(r.Min.X, r.Max.Y)},
		{Min: image.Pt(r.Max.X, r.Min.Y), Max: image.Pt(b.Max.X, r.Max.Y)},
	} {
		if !part.Empty() {
			// Scrims are drawn first onto a cleared frame, so a plain copy
			// is equivalent to compositing and far cheaper per frame.
			draw.Draw(dst.RGBA, part, u, image.Point{}, draw.Src)
			dst.markStable(part)
		}
	}
}

func rectPoints(r image.Rectangle) []contracts.Point {
	return []contracts.Point{
		{X: float64(r.Min.X), Y: float64(r.Min.Y)}, {X: float64(r.Max.X), Y: float64(r.Min.Y)},
		{X: float64(r.Max.X), Y: float64(r.Max.Y)}, {X: float64(r.Min.X), Y: float64(r.Max.Y)},
	}
}

// brackets returns the four L-shaped corner arms of r.
func brackets(r image.Rectangle, arm float64) [][]contracts.Point {
	x0, y0, x1, y1 := float64(r.Min.X), float64(r.Min.Y), float64(r.Max.X), float64(r.Max.Y)
	return [][]contracts.Point{
		{{X: x0, Y: y0 + arm}, {X: x0, Y: y0}, {X: x0 + arm, Y: y0}},
		{{X: x1 - arm, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y0 + arm}},
		{{X: x1, Y: y1 - arm}, {X: x1, Y: y1}, {X: x1 - arm, Y: y1}},
		{{X: x0 + arm, Y: y1}, {X: x0, Y: y1}, {X: x0, Y: y1 - arm}},
	}
}

// segment adds a thick line as a quad.
func segment(p *painter, a, b contracts.Point, w float64) {
	dx, dy := b.X-a.X, b.Y-a.Y
	l := math.Hypot(dx, dy)
	if l == 0 {
		disc(p, a, w/2)
		return
	}
	nx, ny := -dy/l*w/2, dx/l*w/2
	p.polygon([]contracts.Point{
		{X: a.X + nx, Y: a.Y + ny}, {X: b.X + nx, Y: b.Y + ny},
		{X: b.X - nx, Y: b.Y - ny}, {X: a.X - nx, Y: a.Y - ny},
	})
}

// polyline strokes pts with round caps and joints.
func polyline(p *painter, pts []contracts.Point, w float64) {
	p.beginStroke()
	defer p.endStroke()
	disc(p, pts[0], w/2)
	p.fill()
	for i := 1; i < len(pts); i++ {
		segment(p, pts[i-1], pts[i], w)
		p.fill()
		disc(p, pts[i], w/2)
		p.fill()
	}
}

// headGlint lights an arrow's head while the glint passes over it.
func headGlint(p *painter, from, base, to contracts.Point, w float64, an anim, c color.NRGBA) {
	if !an.live {
		return
	}
	total := dist(from, base) + dist(base, to)
	if total == 0 {
		return
	}
	start := dist(from, base) / total
	pos, env := openGlint(an.phase)
	for _, l := range glintLayers {
		lo, hi := max(pos-l.span/2, start), min(pos+l.span/2, 1)
		if hi <= lo {
			continue
		}
		cover := min(1, (hi-lo)/max(1-start, 1e-9)*2)
		p.color(alpha(mix(c, white, l.light), l.alpha*an.appear*cover*env))
		p.polygon(arrowHead(base, to, w))
		p.fill()
	}
}

// arrowHead is the head triangle of an arrow ending at to.
func arrowHead(base, to contracts.Point, w float64) []contracts.Point {
	l := dist(base, to)
	if l == 0 {
		return nil
	}
	ux, uy := (to.X-base.X)/l, (to.Y-base.Y)/l
	half := l * 0.6
	return []contracts.Point{
		to,
		{X: base.X - uy*half, Y: base.Y + ux*half},
		{X: base.X + uy*half, Y: base.Y - ux*half},
	}
}

// shaftEnd is where arrow's shaft meets its head.
func shaftEnd(from, to contracts.Point, w float64) contracts.Point {
	l := math.Hypot(to.X-from.X, to.Y-from.Y)
	if l == 0 {
		return from
	}
	head := min(l, 5*w)
	return contracts.Point{X: to.X - (to.X-from.X)/l*head, Y: to.Y - (to.Y-from.Y)/l*head}
}

// arrow strokes a shaft with a triangular head and returns where the shaft
// meets the head.
func arrow(p *painter, from, to contracts.Point, w float64) contracts.Point {
	dx, dy := to.X-from.X, to.Y-from.Y
	l := math.Hypot(dx, dy)
	if l == 0 {
		return from
	}
	ux, uy := dx/l, dy/l
	head := min(l, 5*w)
	base := contracts.Point{X: to.X - ux*head, Y: to.Y - uy*head}
	polyline(p, []contracts.Point{from, base}, w)
	half := head * 0.6
	p.polygon([]contracts.Point{
		to,
		{X: base.X - uy*half, Y: base.Y + ux*half},
		{X: base.X + uy*half, Y: base.Y - ux*half},
	})
	p.fill()
	return base
}

// cursorSize is the logical width and height of the virtual cursor.
const cursorSize = 18.0

// cursorContours is the logo mark, Unbounded's "↖", flattened to polygons
// in a unit square with the arrow's tip at the origin. Using the logo
// keeps the virtual cursor recognisably Companion's and visually distinct
// from the system pointer (FR-OVERLAY-002).
var cursorContours = func() [][]contracts.Point {
	f, err := sfnt.Parse(fonts.Must(fonts.Display))
	if err != nil {
		panic(err) // embedded font; cannot fail at runtime
	}
	var buf sfnt.Buffer
	gi, err := f.GlyphIndex(&buf, '↖')
	if err != nil || gi == 0 {
		panic("overlay: logo glyph missing from the display font")
	}
	segs, err := f.LoadGlyph(&buf, gi, fixed.I(1000), nil)
	if err != nil {
		panic(err)
	}
	var cs [][]contracts.Point
	var cur []contracts.Point
	last := contracts.Point{}
	pt := func(p fixed.Point26_6) contracts.Point {
		return contracts.Point{X: float64(p.X) / 64, Y: float64(p.Y) / 64}
	}
	// Curves are flattened; the glyph is straight lines, so this is exact.
	for _, sg := range segs {
		switch sg.Op {
		case sfnt.SegmentOpMoveTo:
			if len(cur) > 2 {
				cs = append(cs, cur)
			}
			cur = []contracts.Point{pt(sg.Args[0])}
			last = cur[0]
		case sfnt.SegmentOpLineTo:
			last = pt(sg.Args[0])
			cur = append(cur, last)
		case sfnt.SegmentOpQuadTo:
			c, e := pt(sg.Args[0]), pt(sg.Args[1])
			for i := 1; i <= 8; i++ {
				t := float64(i) / 8
				u := 1 - t
				cur = append(cur, contracts.Point{X: u*u*last.X + 2*u*t*c.X + t*t*e.X, Y: u*u*last.Y + 2*u*t*c.Y + t*t*e.Y})
			}
			last = e
		case sfnt.SegmentOpCubeTo:
			c1, c2, e := pt(sg.Args[0]), pt(sg.Args[1]), pt(sg.Args[2])
			for i := 1; i <= 8; i++ {
				t := float64(i) / 8
				u := 1 - t
				cur = append(cur, contracts.Point{
					X: u*u*u*last.X + 3*u*u*t*c1.X + 3*u*t*t*c2.X + t*t*t*e.X,
					Y: u*u*u*last.Y + 3*u*u*t*c1.Y + 3*u*t*t*c2.Y + t*t*t*e.Y,
				})
			}
			last = e
		}
	}
	if len(cur) > 2 {
		cs = append(cs, cur)
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, c := range cs {
		for _, q := range c {
			minX, minY = min(minX, q.X), min(minY, q.Y)
			maxX, maxY = max(maxX, q.X), max(maxY, q.Y)
		}
	}
	size := max(maxX-minX, maxY-minY)
	for _, c := range cs {
		for i, q := range c {
			c[i] = contracts.Point{X: (q.X - minX) / size, Y: (q.Y - minY) / size}
		}
	}
	return cs
}()

// cursor draws the logo arrow with its tip at at over a soft drop shadow
// that keeps it readable on light content. It glides in towards the
// target and then carries the shimmer.
// Each contour fills on its own because the glyph's shaft and head overlap
// and opposite windings would cancel.
func cursor(dst *canvas, at contracts.Point, k float64, c color.NRGBA, an anim) {
	size := cursorSize * k
	off := 14 * k * (1 - an.appear)
	at = contracts.Point{X: at.X + off, Y: at.Y + off}
	place := func(cont []contracts.Point, dx, dy float64) []contracts.Point {
		pts := make([]contracts.Point, len(cont))
		for i, q := range cont {
			pts[i] = contracts.Point{X: at.X + q.X*size + dx, Y: at.Y + q.Y*size + dy}
		}
		return pts
	}
	pad := 4 * k
	box := image.Rect(int(at.X-pad), int(at.Y-pad), int(at.X+size+pad), int(at.Y+size+2*pad))
	p := newPainter(dst, box)
	draw := func(dx, dy float64) {
		for _, cont := range cursorContours {
			p.polygon(place(cont, dx, dy))
			p.fill()
		}
	}
	p.color(an.fade(color.NRGBA{A: 0x40}))
	draw(0, 2.5*k)
	p.color(an.fade(c))
	draw(0, 0)
	mask := make([][]contracts.Point, len(cursorContours))
	for i, cont := range cursorContours {
		mask[i] = place(cont, 0, 0)
	}
	sweep(dst, box, mask, an, 0.55)
}

// badge is a filled numbered marker for walkthrough steps.
func badge(dst *canvas, at contracts.Point, text string, k float64, c color.NRGBA, an anim, l layer) {
	r := badgeRadius * k * an.grow(0.4)
	if l.live() {
		defer sweep(dst, around(at, r), [][]contracts.Point{circlePoints(at, r, false)}, an, 0.3)
	}
	if !l.static() {
		return
	}
	p := newPainter(dst, around(at, r+glowSpread*k))
	glow(p, an.fade(c), glowSpread*k*0.7, func(extra float64) { disc(p, at, r+extra) })
	p.color(an.fade(c))
	disc(p, at, r)
	p.fill()
	face := textFace(badgePt * k)
	m := face.Metrics()
	d := font.Drawer{Face: face, Dst: dst.RGBA, Src: image.NewUniform(an.fade(labelFg))}
	tw := d.MeasureString(text)
	d.Dot = fixed.Point26_6{
		X: fixed.Int26_6(at.X*64) - tw/2,
		Y: fixed.Int26_6(at.Y*64) + (m.Ascent-m.Descent)/2,
	}
	d.DrawString(text)
}

const (
	labelPt     = 14
	labelHeight = 32
	labelPadX   = 12
	badgePt     = 14
	badgeRadius = 14
)

var labelFont = func() *opentype.Font {
	f, err := opentype.Parse(fonts.Must(fonts.SansMedium))
	if err != nil {
		panic(err) // embedded font; cannot fail at runtime
	}
	return f
}()

// labelFaces caches faces per pixel size; scales are few and fixed.
var labelFaces sync.Map // float64 → font.Face

func textFace(size float64) font.Face {
	if f, ok := labelFaces.Load(size); ok {
		return f.(font.Face)
	}
	f, err := opentype.NewFace(labelFont, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	v, _ := labelFaces.LoadOrStore(size, f)
	return v.(font.Face)
}

// label draws a rounded dark plate with text, its left edge at at.X and
// centred vertically on at.Y so it sits beside a ring. It is kept inside
// the frame when the anchor is near an edge.
func label(dst *canvas, at contracts.Point, text string, k float64, an anim) {
	face := textFace(labelPt * k)
	m := face.Metrics()
	d := font.Drawer{Face: face}
	tw := d.MeasureString(text).Ceil()
	padX, h := int(labelPadX*k), int(labelHeight*k)
	// Entrance: slides in from the left while fading in.
	slide := int(10 * k * (1 - an.appear))
	bg := image.Rect(0, 0, tw+2*padX, h).Add(image.Pt(int(at.X)-slide, int(at.Y)-h/2))
	fb := dst.Bounds()
	if bg.Max.X > fb.Max.X {
		bg = bg.Sub(image.Pt(bg.Max.X-fb.Max.X, 0))
	}
	if bg.Min.X < fb.Min.X {
		bg = bg.Add(image.Pt(fb.Min.X-bg.Min.X, 0))
	}
	if bg.Min.Y < fb.Min.Y {
		bg = bg.Add(image.Pt(0, fb.Min.Y-bg.Min.Y))
	}
	if bg.Max.Y > fb.Max.Y {
		bg = bg.Sub(image.Pt(0, bg.Max.Y-fb.Max.Y))
	}
	plate := roundRect(bg, cornerR*k, 0, false)
	p := newPainter(dst, bg)
	p.color(an.fade(labelBg))
	p.polygon(plate)
	p.fill()
	// Centre the text's ascent+descent box in the plate.
	th := m.Ascent + m.Descent
	baseline := fixed.I(bg.Min.Y) + (fixed.I(h)-th)/2 + m.Ascent
	d.Dst = dst.RGBA
	d.Src = image.NewUniform(an.fade(labelFg))
	d.Dot = fixed.Point26_6{X: fixed.I(bg.Min.X + padX), Y: baseline}
	d.DrawString(text)
	sweep(dst, bg, [][]contracts.Point{plate}, an, 0.10)
}

// parseColor accepts "#RRGGBB" and "#RRGGBBAA"; anything else is the theme
// default so a bad model argument cannot break drawing.
func parseColor(s string) color.NRGBA {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return defaultColor
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return defaultColor
	}
	if len(s) == 7 {
		return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xFF}
	}
	return color.NRGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}
}
