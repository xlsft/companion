package overlay

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomedium"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/xlsft/openclick/pkg/contracts"
)

var (
	defaultColor = color.NRGBA{R: 0xFF, G: 0x8A, B: 0x00, A: 0xFF}
	labelBg      = color.NRGBA{R: 0x11, G: 0x11, B: 0x11, A: 0xE6}
	labelFg      = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
)

const defaultStroke = 4.0 // logical pixels

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
	w := s.style.StrokeWidth
	if w <= 0 {
		w = defaultStroke
	}
	w *= s.scale
	p := newPainter(dst, c)
	switch s.kind {
	case KindPoint:
		cursor(p, s.pts[0], 14*s.scale)
	case KindRing:
		annulus(p, s.pts[0], s.radius, w)
		if s.style.Emphasis {
			annulus(p, s.pts[0], s.radius+3*w, w/2)
		}
	case KindCircle:
		annulus(p, s.pts[0], s.radius, w)
	case KindRect:
		a, b := s.pts[0], s.pts[1]
		polyline(p, []contracts.Point{a, {X: b.X, Y: a.Y}, b, {X: a.X, Y: b.Y}, a}, w)
	case KindArrow:
		arrow(p, s.pts[0], s.pts[1], w)
	case KindFreehand:
		polyline(p, s.pts, w)
	case KindLabel:
		label(dst, s.pts[0], s.text, s.scale)
		return
	case KindImageCard:
		// Image cards show the referenced image in the panel UI; on the
		// overlay they are a placeholder frame with the reference name.
		label(dst, s.pts[0], s.text, s.scale)
		return
	}
	p.fill()
}

// painter accumulates paths and composites them with fill. The rasterizer
// takes |sum| of signed coverage, so paths that must cancel (ring holes)
// share one fill, while independent pieces get their own fill to avoid
// opposite windings cancelling each other.
type painter struct {
	dst *image.RGBA
	r   *vector.Rasterizer
	src image.Image
}

func newPainter(dst *image.RGBA, c color.NRGBA) *painter {
	b := dst.Bounds()
	return &painter{dst: dst, r: vector.NewRasterizer(b.Dx(), b.Dy()), src: image.NewUniform(c)}
}

func (p *painter) polygon(pts []contracts.Point) {
	if len(pts) < 3 {
		return
	}
	p.r.MoveTo(float32(pts[0].X), float32(pts[0].Y))
	for _, q := range pts[1:] {
		p.r.LineTo(float32(q.X), float32(q.Y))
	}
	p.r.ClosePath()
}

func (p *painter) fill() {
	b := p.dst.Bounds()
	p.r.Draw(p.dst, b, p.src, image.Point{})
	p.r.Reset(b.Dx(), b.Dy())
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

// annulus fills a ring as outer circle plus reversed inner circle; the
// rasterizer's signed coverage cancels the inside.
func annulus(p *painter, c contracts.Point, r, w float64) {
	p.polygon(circlePoints(c, r+w/2, false))
	if inner := r - w/2; inner > 0 {
		p.polygon(circlePoints(c, inner, true))
	}
}

func disc(p *painter, c contracts.Point, r float64) {
	p.polygon(circlePoints(c, r, false))
}

// segment fills a thick line as a quad with round joints.
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

func polyline(p *painter, pts []contracts.Point, w float64) {
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
	segment(p, from, base, w)
	p.fill()
	half := head * 0.6
	p.polygon([]contracts.Point{
		to,
		{X: base.X - uy*half, Y: base.Y + ux*half},
		{X: base.X + uy*half, Y: base.Y - ux*half},
	})
}

// cursor draws a virtual pointer with its tip at at; it is visually
// distinct from the system pointer (FR-OVERLAY-002).
func cursor(p *painter, at contracts.Point, size float64) {
	p.polygon([]contracts.Point{
		at,
		{X: at.X, Y: at.Y + size*1.4},
		{X: at.X + size*0.4, Y: at.Y + size},
		{X: at.X + size, Y: at.Y + size},
	})
}

const labelPt = 15 // logical size; multiplied by the display scale

var labelFont = func() *opentype.Font {
	f, err := opentype.Parse(gomedium.TTF)
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
	labelFaces.Store(size, f)
	return f
}

func label(dst *image.RGBA, at contracts.Point, text string, scale float64) {
	face := labelFace(scale)
	m := face.Metrics()
	d := font.Drawer{Face: face}
	tw := d.MeasureString(text).Ceil()
	pad := int(6 * scale)
	h := (m.Ascent + m.Descent).Ceil()
	// Center the label vertically on the anchor so it sits beside a ring.
	top := int(at.Y) - h/2 - pad
	bg := image.Rect(int(at.X), top, int(at.X)+tw+2*pad, top+h+2*pad)
	draw.Draw(dst, bg, image.NewUniform(labelBg), image.Point{}, draw.Over)
	d.Dst = dst
	d.Src = image.NewUniform(labelFg)
	d.Dot = fixed.Point26_6{X: fixed.I(bg.Min.X + pad), Y: fixed.I(bg.Min.Y+pad) + m.Ascent}
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
