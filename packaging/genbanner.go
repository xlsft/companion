//go:build ignore

// genbanner renders the README banner and the social preview: the logo (the
// bare "↖" mark and the "companion" wordmark in Unbounded, laid out as
// kit.Logo does) and the text in Google Sans, all as SVG paths (same
// approach as genicon.go), so GitHub shows it without the fonts.
//
//	go run packaging/genbanner.go > docs/assets/banner.svg              # variant b
//	go run packaging/genbanner.go -variant a > docs/assets/banner.svg   # a | b | c
//	go run packaging/genbanner.go -h 640 > social.svg && rsvg-convert social.svg -o docs/assets/social-preview.png
//
// a: centred logo on dark with a soft violet glow and the pitch under it;
// b: logo, pitch and chips left, an app window with a pointing hint right;
// c: light, a faint grid, the logo left and a large hint ring right.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	accent   = "#8b5cf6"
	headline = "The friend who knows everything."
	sub      = "Ask how to do anything. Companion watches your screen with you and guides you step by step."
)

var (
	// subLines is sub broken where the banner has room.
	subLines = []string{"Ask how to do anything. Companion watches your screen", "with you and guides you step by step."}
	chips    = []string{"Works everywhere", "Your voice stays on your computer", "No account"}
)

// hsl formats an HSL colour (degrees, percent, percent) as #rrggbb, so the
// banner can use the app palette's values as written in theme.go.
func hsl(h, s, l float64) string {
	s, l = s/100, l/100
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	to := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", to(r), to(g), to(b))
}

type face struct {
	f   *sfnt.Font
	buf sfnt.Buffer
}

func load(path string) *face {
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		fail(err)
	}
	return &face{f: f}
}

// path returns the outline of s at the given pixel size with its baseline
// at (x, y), and the advance width.
func (fc *face) path(s string, px, x, y float64) (string, float64) {
	ppem := fixed.Int26_6(px * 64)
	var d strings.Builder
	pen := x
	prev := sfnt.GlyphIndex(0)
	for _, r := range s {
		idx, err := fc.f.GlyphIndex(&fc.buf, r)
		if err != nil || idx == 0 {
			fail(fmt.Errorf("glyph %q missing", r))
		}
		if prev != 0 {
			if k, err := fc.f.Kern(&fc.buf, prev, idx, ppem, font.HintingNone); err == nil {
				pen += float64(k) / 64
			}
		}
		segs, err := fc.f.LoadGlyph(&fc.buf, idx, ppem, nil)
		if err != nil {
			fail(err)
		}
		writeSegs(&d, segs, pen, y)
		adv, err := fc.f.GlyphAdvance(&fc.buf, idx, ppem, font.HintingNone)
		if err != nil {
			fail(err)
		}
		pen += float64(adv) / 64
		prev = idx
	}
	return d.String(), pen - x
}

// width measures s without emitting it.
func (fc *face) width(s string, px float64) float64 {
	_, w := fc.path(s, px, 0, 0)
	return w
}

// metrics are the font's ascent and descent at px, which size a line box.
func (fc *face) metrics(px float64) (asc, desc float64) {
	m, err := fc.f.Metrics(&fc.buf, fixed.Int26_6(px*64), font.HintingNone)
	if err != nil {
		fail(err)
	}
	return float64(m.Ascent) / 64, float64(m.Descent) / 64
}

// baseline is where text of px sits in a line box of height lh at top, the
// way CSS and Gio centre the font's ascent+descent in it.
func (fc *face) baseline(top, lh, px float64) float64 {
	asc, desc := fc.metrics(px)
	return top + (lh-(asc+desc))/2 + asc
}

// inkMin is the top-left of the first glyph's ink box relative to its origin.
func (fc *face) inkMin(r rune, px float64) (float64, float64) {
	idx, err := fc.f.GlyphIndex(&fc.buf, r)
	if err != nil || idx == 0 {
		fail(fmt.Errorf("glyph %q missing", r))
	}
	segs, err := fc.f.LoadGlyph(&fc.buf, idx, fixed.Int26_6(px*64), nil)
	if err != nil {
		fail(err)
	}
	minX, minY := 1e9, 1e9
	for _, s := range segs {
		n := map[sfnt.SegmentOp]int{sfnt.SegmentOpMoveTo: 1, sfnt.SegmentOpLineTo: 1, sfnt.SegmentOpQuadTo: 2, sfnt.SegmentOpCubeTo: 3}[s.Op]
		for _, p := range s.Args[:n] {
			minX, minY = min(minX, float64(p.X)/64), min(minY, float64(p.Y)/64)
		}
	}
	return minX, minY
}

func writeSegs(d *strings.Builder, segs sfnt.Segments, ox, oy float64) {
	tr := func(p fixed.Point26_6) string {
		return fmt.Sprintf("%.2f %.2f", ox+float64(p.X)/64, oy+float64(p.Y)/64)
	}
	open := false
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			if open {
				d.WriteString("Z")
			}
			d.WriteString("M" + tr(s.Args[0]))
			open = true
		case sfnt.SegmentOpLineTo:
			d.WriteString("L" + tr(s.Args[0]))
		case sfnt.SegmentOpQuadTo:
			d.WriteString("Q" + tr(s.Args[0]) + " " + tr(s.Args[1]))
		case sfnt.SegmentOpCubeTo:
			d.WriteString("C" + tr(s.Args[0]) + " " + tr(s.Args[1]) + " " + tr(s.Args[2]))
		}
	}
	if open {
		d.WriteString("Z")
	}
}

type banner struct {
	w, h          float64
	unb, sans, md *face
	out           strings.Builder
}

func (b *banner) printf(format string, args ...any) { fmt.Fprintf(&b.out, format, args...) }

func (b *banner) text(f *face, s string, px, x, y float64, fill string) float64 {
	d, w := f.path(s, px, x, y)
	b.printf(`  <path d="%s" fill="%s"/>`+"\n", d, fill)
	return w
}

func (b *banner) rect(x, y, w, h, rx float64, fill string) {
	b.printf(`  <rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%g" fill="%s"/>`+"\n", x, y, w, h, rx, fill)
}

// logoWidth is the logo's width with the wordmark at px.
func (b *banner) logoWidth(px float64) float64 {
	return b.unb.width("↖", px*1.2) + px*0.6 + b.unb.width("companion", px)
}

// logo draws the logo as kit.Logo lays it out: the mark at 1.2× the
// wordmark and 0.6× apart, both label boxes centred on the row's middle cy.
func (b *banner) logo(x, cy, px float64, ink string) {
	markPx := px * 1.2
	asc, desc := b.unb.metrics(1)
	mark, mw := b.unb.path("↖", markPx, x, cy+(asc-desc)/2*markPx)
	word, _ := b.unb.path("companion", px, x+mw+px*0.6, cy+(asc-desc)/2*px)
	b.printf(`  <path d="%s" fill="%s"/>`+"\n", mark, accent)
	b.printf(`  <path d="%s" fill="%s"/>`+"\n", word, ink)
}

// chip is an outlined tag; it returns its width.
func (b *banner) chip(s string, x, y float64, border, ink string) float64 {
	const px, padX, h = 13.0, 12.0, 28.0
	w := padX*2 + b.sans.width(s, px)
	b.printf(`  <rect x="%.1f" y="%.1f" width="%.1f" height="%g" rx="6" fill="none" stroke="%s" stroke-width="1"/>`+"\n", x+0.5, y+0.5, w-1, h-1, border)
	b.text(b.sans, s, px, x+padX, b.sans.baseline(y, h, px), ink)
	return w
}

// hint is the overlay's pointing hint as the app draws it: a glowing ring,
// the logo cursor with its tip at the target and a dark label plate beside
// the ring. size scales the cursor and label with the ring.
func (b *banner) hint(cx, cy, r, stroke float64, label string) {
	b.printf(`  <circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="%.1f" stroke-opacity="0.6" filter="url(#glow-blur)"/>`+"\n", cx, cy, r, accent, stroke*2.5)
	b.printf(`  <circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="%s" stroke-width="%.1f"/>`+"\n", cx, cy, r, accent, stroke)
	k := r / 41
	curPx := 26 * k
	minX, minY := b.unb.inkMin('↖', curPx)
	tx, ty := cx+3*k-minX, cy+2*k-minY
	cur, _ := b.unb.path("↖", curPx, tx, ty)
	b.printf(`  <path d="%s" fill="#000" fill-opacity="0.5" transform="translate(0 %.1f)" filter="url(#soft)"/>`+"\n", cur, 2*k)
	b.printf(`  <path d="%s" fill="%s"/>`+"\n", cur, accent)
	if label == "" {
		return
	}
	px, h, pad := 13*k, 30*k, 12*k
	tw := b.md.width(label, px)
	lx := cx + r + stroke/2 + 14*k
	b.printf(`  <rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="%.1f" fill="#111111" fill-opacity="0.92"/>`+"\n", lx, cy-h/2, tw+2*pad, h, 6*k)
	b.text(b.md, label, px, lx+pad, b.md.baseline(cy-h/2, h, px), "#ffffff")
}

func (b *banner) start(bg string) {
	b.printf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]g" height="%[2]g" viewBox="0 0 %[1]g %[2]g" role="img" aria-label="companion: %[3]s %[4]s">
  <defs>
    <clipPath id="frame"><rect width="%[1]g" height="%[2]g" rx="12"/></clipPath>
    <filter id="glow-blur" x="-50%%" y="-50%%" width="200%%" height="200%%"><feGaussianBlur stdDeviation="9"/></filter>
    <filter id="soft" x="-50%%" y="-50%%" width="200%%" height="200%%"><feGaussianBlur stdDeviation="1.5"/></filter>
    <filter id="bubble" x="-30%%" y="-60%%" width="160%%" height="260%%"><feDropShadow dx="0" dy="12" stdDeviation="14" flood-color="#000" flood-opacity="0.45"/></filter>
    <filter id="shadow" x="-30%%" y="-30%%" width="160%%" height="170%%"><feDropShadow dx="0" dy="30" stdDeviation="30" flood-color="#000" flood-opacity="0.5"/></filter>
  </defs>
  <g clip-path="url(#frame)">
  <rect width="%[1]g" height="%[2]g" fill="%[5]s"/>
`, b.w, b.h, headline, sub, bg)
}

func (b *banner) glow(cx, cy, rx, ry float64, c string, opacity float64) {
	b.printf(`  <radialGradient id="g%[1]d" gradientUnits="userSpaceOnUse" cx="%.0[2]f" cy="%.0[3]f" r="%.0[4]f" gradientTransform="translate(%.0[2]f %.0[3]f) scale(1 %.3[5]f) translate(-%.0[2]f -%.0[3]f)">
    <stop offset="0" stop-color="%[6]s" stop-opacity="%[7]g"/>
    <stop offset="1" stop-color="%[6]s" stop-opacity="0"/>
  </radialGradient>
  <rect width="%[8]g" height="%[9]g" fill="url(#g%[1]d)"/>
`, len(b.out.String()), cx, cy, rx, ry/rx, c, opacity, b.w, b.h)
}

// variantA: centred logo with a soft glow and the pitch under it.
func (b *banner) variantA() {
	b.start(hsl(240, 6, 7))
	b.glow(b.w/2, b.h*0.45, b.w*0.3, b.h*0.9, hsl(258, 45, 16), 1)
	cy := b.h / 2
	const wordPx, subPx = 72.0, 20.0
	b.logo((b.w-b.logoWidth(wordPx))/2, cy-30, wordPx, hsl(240, 15, 99))
	tw := b.sans.width(headline, subPx)
	b.text(b.sans, headline, subPx, (b.w-tw)/2, cy+50, hsl(240, 5, 70))
}

// variantB: logo, headline, sub line and chips left; an app window with
// the pointing hint over one of its buttons right. Laid out on the owner's
// 1280×320 reference; a taller canvas (the social preview) centres the text
// and lets the window run off the bottom edge.
func (b *banner) variantB() {
	b.start(hsl(240, 6, 7))
	b.glow(b.w*0.75, b.h*0.6, 420, 300, hsl(258, 45, 16), 0.9)
	oy := (b.h - 320) / 2
	const x = 72.0
	// The left column is a flex column of these line boxes with 18px gaps,
	// centred vertically.
	top := oy + (320-215)/2
	b.logo(x, top+29, 48, hsl(240, 15, 99))
	top += 58 + 18
	b.text(b.md, headline, 22, x, b.md.baseline(top, 22*1.35, 22), hsl(240, 15, 99))
	top += 22*1.35 + 18 - 8
	for i, l := range subLines {
		b.text(b.sans, l, 17, x, b.sans.baseline(top+float64(i)*17*1.45, 17*1.45, 17), hsl(240, 5, 72))
	}
	top += 2*17*1.45 + 18 + 4
	cx := x
	for _, c := range chips {
		cx += b.chip(c, cx, top, hsl(240, 5, 20), hsl(240, 5, 78)) + 8
	}

	// The window is a calm stand-in for any app, not a real one.
	wx, wy, ww := 700.0, oy+40, 520.0
	wh := b.h - wy + 20
	bar := func(x, y, w, h float64, fill string) { b.rect(x, y, w, h, h/2, fill) }
	b.printf(`  <g filter="url(#shadow)"><rect x="%.1f" y="%.1f" width="%g" height="%.1f" rx="10" fill="%s" stroke="%s" stroke-width="1"/></g>`+"\n", wx, wy, ww, wh, hsl(240, 5, 10), hsl(240, 5, 18))
	for i := range 3 {
		b.printf(`  <circle cx="%.1f" cy="%.1f" r="4.5" fill="%s"/>`+"\n", wx+18.5+float64(i)*15, wy+17, hsl(240, 5, 24))
	}
	bar(wx+73, wy+13, 140, 8, hsl(240, 5, 18))
	b.rect(wx, wy+34, ww, 1, 0, hsl(240, 5, 16))
	b.rect(wx+120, wy+35, 1, wh-35, 0, hsl(240, 5, 16))
	for i, w := range []float64{1, 0.7, 0.85, 0.6} {
		fill := hsl(240, 5, 17)
		if i == 0 {
			fill = hsl(240, 5, 20)
		}
		bar(wx+12, wy+51+float64(i)*20, 96*w, 8, fill)
	}
	px, py := wx+139, wy+53
	bar(px, py, 363*0.55, 10, hsl(240, 5, 20))
	bar(px, py+24, 363*0.80, 8, hsl(240, 5, 16))
	bar(px, py+46, 363*0.72, 8, hsl(240, 5, 16))
	by := py + 78
	b.rect(px, by, 96, 30, 6, hsl(240, 5, 16))
	b.rect(px+106, by, 96, 30, 6, hsl(240, 5, 16))
	b.hint(px+106+48, by+15, 41, 4, "Export")
	b.question(846, oy+92, "How do I export this?")
}

// micIcon is MingCute's mic-line on its 24×24 grid, as the chat uses it.
const micIcon = "M19.07 12.01a1 1 0 0 1 .85 1.132A8.004 8.004 0 0 1 13 19.938V21a1 1 0 1 1-2 0v-1.062a8.005 8.005 0 0 1-6.919-6.796a1 1 0 0 1 1.98-.284a6.002 6.002 0 0 0 11.878 0a1 1 0 0 1 1.132-.848ZM12 2a5 5 0 0 1 5 5v5a5 5 0 0 1-10 0V7a5 5 0 0 1 5-5m0 2a3 3 0 0 0-3 3v5a3 3 0 1 0 6 0V7a3 3 0 0 0-3-3"

// question is the user's spoken question as a chat bubble, its lower right
// corner tucked like the chat's own user bubbles.
func (b *banner) question(x, y float64, s string) {
	const px, padX, padY, icon, gap = 15.0, 14.0, 10.0, 14.0, 8.0
	lh := px * 1.2
	w := padX*2 + icon + gap + b.sans.width(s, px)
	h := padY*2 + lh
	r, rs := 14.0, 4.0
	d := fmt.Sprintf("M%.1f %.1fH%.1fA%g %g 0 0 1 %.1f %.1fV%.1fA%g %g 0 0 1 %.1f %.1fH%.1fA%g %g 0 0 1 %.1f %.1fV%.1fA%g %g 0 0 1 %.1f %.1fZ",
		x+r, y, x+w-r, r, r, x+w, y+r, y+h-rs, rs, rs, x+w-rs, y+h, x+r, r, r, x, y+h-r, y+r, r, r, x+r, y)
	b.printf(`  <path d="%s" fill="%s" stroke="%s" stroke-width="1" filter="url(#bubble)"/>`+"\n", d, hsl(258, 32, 22), hsl(258, 40, 32))
	b.printf(`  <path d="%s" fill="#c4b5fd" transform="translate(%.1f %.1f) scale(%.4f)"/>`+"\n", micIcon, x+padX, y+(h-icon)/2, icon/24)
	b.text(b.sans, s, px, x+padX+icon+gap, b.sans.baseline(y+padY, lh, px), hsl(240, 15, 99))
}

// variantC: light, a faint grid, the logo left and a large hint ring right.
func (b *banner) variantC() {
	b.start(hsl(240, 6, 97))
	b.printf(`  <pattern id="grid" width="32" height="32" patternUnits="userSpaceOnUse"><path d="M32 0.5H0.5V32" fill="none" stroke="%[3]s" stroke-width="1"/></pattern>
  <rect width="%[1]g" height="%[2]g" fill="url(#grid)"/>
`, b.w, b.h, hsl(240, 6, 92))
	b.glow(b.w*0.3, b.h*0.5, b.w*0.45, b.h*0.6, hsl(240, 6, 97), 1)
	cy := b.h / 2
	b.logo(96, cy-24, 58, hsl(240, 10, 6))
	b.text(b.sans, headline, 19, 96, cy+44, hsl(240, 4, 40))
	b.hint(b.w*0.81, cy, 66, 5, "")
}

func main() {
	variant := flag.String("variant", "b", "banner variant: a, b or c")
	w := flag.Float64("w", 1280, "width")
	h := flag.Float64("h", 320, "height (at least 320)")
	flag.Parse()
	b := &banner{
		w: *w, h: max(*h, 320),
		unb:  load("internal/assets/fonts/Unbounded-SemiBold.ttf"),
		sans: load("internal/assets/fonts/GoogleSans-Regular.ttf"),
		md:   load("internal/assets/fonts/GoogleSans-Medium.ttf"),
	}
	switch *variant {
	case "a":
		b.variantA()
	case "b":
		b.variantB()
	case "c":
		b.variantC()
	default:
		fail(fmt.Errorf("unknown variant %q", *variant))
	}
	b.out.WriteString("  </g>\n</svg>\n")
	os.Stdout.WriteString(b.out.String())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genbanner:", err)
	os.Exit(1)
}
