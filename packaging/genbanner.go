//go:build ignore

// genbanner renders the README banner: the logo mark and the "companion"
// wordmark in Unbounded, and the tagline in Google Sans, all as SVG paths
// (same approach as genicon.go), so GitHub shows it without the fonts.
//
//	go run packaging/genbanner.go > docs/assets/banner.svg
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	width, height = 1600.0, 480.0
	bg            = "#09090b" // zinc-950
	tile          = "#18181b" // zinc-900
	accent        = "#8b5cf6"
	ink           = "#fafafa"
	muted         = "#a1a1aa" // zinc-400

	tagline = "Ask about what is on your screen. Local speech, your own model, no backend."
)

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

func main() {
	unb := load("internal/assets/fonts/Unbounded-SemiBold.ttf")
	sans := load("internal/assets/fonts/GoogleSans-Regular.ttf")

	const (
		wordPx = 112.0
		tagPx  = 30.0
		tileSz = 132.0
		gap    = 40.0
	)
	wordW := unb.width("companion", wordPx)
	rowW := tileSz + gap + wordW
	x0 := (width - rowW) / 2
	tileY := 132.0
	// Optical centre of lowercase letters: baseline sits a little below the
	// tile's middle plus half the x-height.
	base := tileY + tileSz/2 + wordPx*0.36

	// The mark is the icon's glyph, centred in its tile like genicon does.
	var b sfnt.Buffer
	idx, err := unb.f.GlyphIndex(&b, '↖')
	if err != nil || idx == 0 {
		fail(fmt.Errorf("glyph U+2196 missing"))
	}
	segs, err := unb.f.LoadGlyph(&b, idx, fixed.Int26_6(124*64), nil)
	if err != nil {
		fail(err)
	}
	minX, minY, maxX, maxY := 1e9, 1e9, -1e9, -1e9
	for _, s := range segs {
		n := 3
		switch s.Op {
		case sfnt.SegmentOpMoveTo, sfnt.SegmentOpLineTo:
			n = 1
		case sfnt.SegmentOpQuadTo:
			n = 2
		}
		for _, p := range s.Args[:n] {
			x, y := float64(p.X)/64, float64(p.Y)/64
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	var mark strings.Builder
	writeSegs(&mark, segs,
		x0+(tileSz-(maxX-minX))/2-minX,
		tileY+(tileSz-(maxY-minY))/2-minY)

	word, _ := unb.path("companion", wordPx, x0+tileSz+gap, base)
	tagW := sans.width(tagline, tagPx)
	tag, _ := sans.path(tagline, tagPx, (width-tagW)/2, 366)

	fmt.Printf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]g" height="%[2]g" viewBox="0 0 %[1]g %[2]g" role="img" aria-label="companion: %[3]s">
  <defs>
    <radialGradient id="glow" cx="50%%" cy="38%%" r="60%%">
      <stop offset="0" stop-color="%[4]s" stop-opacity="0.22"/>
      <stop offset="1" stop-color="%[4]s" stop-opacity="0"/>
    </radialGradient>
  </defs>
  <rect width="%[1]g" height="%[2]g" fill="%[5]s"/>
  <rect width="%[1]g" height="%[2]g" fill="url(#glow)"/>
  <rect x="%.2[6]f" y="%.2[7]f" width="%[8]g" height="%[8]g" rx="30" fill="%[9]s" stroke="#27272a" stroke-width="2"/>
  <path d="%[10]s" fill="%[4]s"/>
  <path d="%[11]s" fill="%[12]s"/>
  <path d="%[13]s" fill="%[14]s"/>
</svg>
`, width, height, tagline, accent, bg, x0, tileY, tileSz, tile, mark.String(), word, ink, tag, muted)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genbanner:", err)
	os.Exit(1)
}
