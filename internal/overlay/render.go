package overlay

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"sync"

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
	scale  float64 // native pixels per logical pixel of the display
}

// render draws shapes onto a transparent frame of the given native size.
func render(size contracts.PixelSize, shapes []shape) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, size.W, size.H))
	for _, s := range shapes {
		drawShape(frame, s)
	}
	return frame
}

func drawShape(dst *image.RGBA, s shape) {
	c := parseColor(s.style.Color)
	k := s.scale
	w := s.style.StrokeWidth
	if w <= 0 {
		w = defaultStroke
		if s.kind == KindRect {
			w = rectStroke
		}
	}
	w *= k
	switch s.kind {
	case KindPoint:
		cursor(dst, s.pts[0], k, c)
	case KindRing:
		ctr := s.pts[0]
		p := newPainter(dst, around(ctr, s.radius+w/2+glowSpread*k))
		if s.style.Emphasis {
			// Like CSS box-shadow, the glow spreads outwards only.
			glow(p, c, glowSpread*k, func(extra float64) { annulus(p, ctr, s.radius+extra/2, w+extra) })
		}
		p.color(c)
		annulus(p, ctr, s.radius, w)
		p.fill()
	case KindCircle:
		ctr := s.pts[0]
		p := newPainter(dst, around(ctr, s.radius+w))
		p.color(c)
		if s.style.Dashed {
			dashedCircle(p, ctr, s.radius, w/2, dashLen*k, dashGap*k)
		} else {
			annulus(p, ctr, s.radius, w)
			p.fill()
		}
	case KindRect:
		a, b := s.pts[0], s.pts[1]
		r := image.Rect(int(a.X), int(a.Y), int(b.X), int(b.Y)).Canon()
		if s.style.Emphasis {
			dimOutside(dst, r, cornerR*k)
		}
		p := newPainter(dst, r.Inset(-int(w+glowSpread*k+1)))
		glow(p, c, glowSpread*k, func(extra float64) { roundRectBand(p, r, cornerR*k, -w/2, w/2+extra) })
		p.color(c)
		roundRectStroke(p, r, cornerR*k, w)
		p.fill()
	case KindArrow:
		a, b := s.pts[0], s.pts[1]
		box := image.Rect(int(a.X), int(a.Y), int(b.X), int(b.Y)).Canon().Inset(-int(6*w + 2))
		p := newPainter(dst, box)
		p.color(c)
		arrow(p, a, b, w)
	case KindFreehand:
		p := newPainter(dst, bounds(s.pts).Inset(-int(w+2)))
		p.color(c)
		polyline(p, s.pts, w)
	case KindLabel, KindImageCard:
		// Image cards show the referenced image in the panel UI; on the
		// overlay they are a label with the reference name.
		label(dst, s.pts[0], s.text, k)
	}
}

// painter accumulates paths and composites them with fill, rasterizing
// only the shape's bounding box: a full-display rasterizer per fill made a
// single hint cost tens of milliseconds on large screens. The rasterizer
// takes |sum| of signed coverage, so paths that must cancel (ring holes)
// share one fill, while independent pieces get their own fill to avoid
// opposite windings cancelling each other.
type painter struct {
	dst *image.RGBA
	box image.Rectangle
	r   *vector.Rasterizer
	src *image.Uniform
}

func newPainter(dst *image.RGBA, box image.Rectangle) *painter {
	box = box.Intersect(dst.Bounds())
	p := &painter{dst: dst, box: box, src: image.NewUniform(color.NRGBA{})}
	if !box.Empty() {
		p.r = vector.NewRasterizer(box.Dx(), box.Dy())
	}
	return p
}

func (p *painter) color(c color.NRGBA) { p.src.C = c }

func (p *painter) polygon(pts []contracts.Point) {
	if len(pts) < 3 || p.r == nil {
		return
	}
	ox, oy := float64(p.box.Min.X), float64(p.box.Min.Y)
	p.r.MoveTo(float32(pts[0].X-ox), float32(pts[0].Y-oy))
	for _, q := range pts[1:] {
		p.r.LineTo(float32(q.X-ox), float32(q.Y-oy))
	}
	p.r.ClosePath()
}

func (p *painter) fill() {
	if p.r == nil {
		return
	}
	p.r.Draw(p.dst, p.box, p.src, image.Point{})
	p.r.Reset(p.box.Dx(), p.box.Dy())
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
func dashedCircle(p *painter, c contracts.Point, r, w, dash, gap float64) {
	circ := 2 * math.Pi * r
	n := max(4, int(math.Round(circ/(dash+gap))))
	step := 2 * math.Pi / float64(n)
	on := step * dash / (dash + gap)
	for i := range n {
		a0 := float64(i) * step
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
// region during walkthroughs.
func dimOutside(dst *image.RGBA, r image.Rectangle, rad float64) {
	p := newPainter(dst, dst.Bounds())
	b := dst.Bounds()
	p.color(alpha(color.NRGBA{A: 0xFF}, dimAlpha))
	p.polygon([]contracts.Point{
		{X: float64(b.Min.X), Y: float64(b.Min.Y)}, {X: float64(b.Max.X), Y: float64(b.Min.Y)},
		{X: float64(b.Max.X), Y: float64(b.Max.Y)}, {X: float64(b.Min.X), Y: float64(b.Max.Y)},
	})
	p.polygon(roundRect(r, rad, 0, true))
	p.fill()
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
	disc(p, pts[0], w/2)
	p.fill()
	for i := 1; i < len(pts); i++ {
		segment(p, pts[i-1], pts[i], w)
		p.fill()
		disc(p, pts[i], w/2)
		p.fill()
	}
}

func arrow(p *painter, from, to contracts.Point, w float64) {
	dx, dy := to.X-from.X, to.Y-from.Y
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
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
// that keeps it readable on light content.
// Each contour fills on its own because the glyph's shaft and head overlap
// and opposite windings would cancel.
func cursor(dst *image.RGBA, at contracts.Point, k float64, c color.NRGBA) {
	size := cursorSize * k
	place := func(cont []contracts.Point, dx, dy float64) []contracts.Point {
		pts := make([]contracts.Point, len(cont))
		for i, q := range cont {
			pts[i] = contracts.Point{X: at.X + q.X*size + dx, Y: at.Y + q.Y*size + dy}
		}
		return pts
	}
	pad := 4 * k
	p := newPainter(dst, image.Rect(int(at.X-pad), int(at.Y-pad), int(at.X+size+pad), int(at.Y+size+2*pad)))
	draw := func(dx, dy float64) {
		for _, cont := range cursorContours {
			p.polygon(place(cont, dx, dy))
			p.fill()
		}
	}
	p.color(color.NRGBA{A: 0x40})
	draw(0, 2.5*k)
	p.color(c)
	draw(0, 0)
}

const (
	labelPt     = 14
	labelHeight = 32
	labelPadX   = 12
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

func labelFace(scale float64) font.Face {
	size := labelPt * scale
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
func label(dst *image.RGBA, at contracts.Point, text string, k float64) {
	face := labelFace(k)
	m := face.Metrics()
	d := font.Drawer{Face: face}
	tw := d.MeasureString(text).Ceil()
	padX, h := int(labelPadX*k), int(labelHeight*k)
	bg := image.Rect(0, 0, tw+2*padX, h).Add(image.Pt(int(at.X), int(at.Y)-h/2))
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
	p := newPainter(dst, bg)
	p.color(labelBg)
	p.polygon(roundRect(bg, cornerR*k, 0, false))
	p.fill()
	// Centre the text's ascent+descent box in the plate.
	th := m.Ascent + m.Descent
	baseline := fixed.I(bg.Min.Y) + (fixed.I(h)-th)/2 + m.Ascent
	d.Dst = dst
	d.Src = image.NewUniform(labelFg)
	d.Dot = fixed.Point26_6{X: fixed.I(bg.Min.X + padX), Y: baseline}
	d.DrawString(text)
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
