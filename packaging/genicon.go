//go:build ignore

// genicon renders the logo (the Unbounded "↖" glyph in the accent violet) to
// SVG paths, so the icon needs neither a font nor a rasteriser at runtime.
//
//	go run packaging/genicon.go > packaging/companion.svg
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	fontPath = "internal/assets/fonts/Unbounded-SemiBold.ttf"
	size     = 512.0 // viewBox edge
	glyphPx  = 300.0 // target glyph height inside the tile
)

func main() {
	data, err := os.ReadFile(fontPath)
	if err != nil {
		fail(err)
	}
	f, err := sfnt.Parse(data)
	if err != nil {
		fail(err)
	}
	var b sfnt.Buffer
	const ppem = 1000
	idx, err := f.GlyphIndex(&b, '↖')
	if err != nil || idx == 0 {
		fail(fmt.Errorf("glyph U+2196 missing"))
	}
	segs, err := f.LoadGlyph(&b, idx, ppem, nil)
	if err != nil {
		fail(err)
	}

	// Bounds first, to centre the glyph in the tile.
	minX, minY, maxX, maxY := 1e9, 1e9, -1e9, -1e9
	for _, s := range segs {
		for _, p := range s.Args[:argc(s.Op)] {
			x, y := float64(p.X)/64, float64(p.Y)/64
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	scale := glyphPx / (maxY - minY)
	ox := (size - (maxX-minX)*scale) / 2
	oy := (size - (maxY-minY)*scale) / 2
	// sfnt outlines already have y pointing down.
	tr := func(p fixed.Point26_6) string {
		x := ox + (float64(p.X)/64-minX)*scale
		y := oy + (float64(p.Y)/64-minY)*scale
		return fmt.Sprintf("%.1f %.1f", x, y)
	}

	var d strings.Builder
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			// sfnt contours are implicitly closed.
			if d.Len() > 0 {
				d.WriteString("Z")
			}
			d.WriteString("M" + tr(s.Args[0]))
		case sfnt.SegmentOpLineTo:
			d.WriteString("L" + tr(s.Args[0]))
		case sfnt.SegmentOpQuadTo:
			d.WriteString("Q" + tr(s.Args[0]) + " " + tr(s.Args[1]))
		case sfnt.SegmentOpCubeTo:
			d.WriteString("C" + tr(s.Args[0]) + " " + tr(s.Args[1]) + " " + tr(s.Args[2]))
		}
	}
	d.WriteString("Z")

	fmt.Printf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" width="512" height="512">
  <rect width="512" height="512" rx="112" fill="#14111f"/>
  <path d="%s" fill="#8b5cf6" fill-rule="nonzero"/>
</svg>
`, d.String())
}

func argc(op sfnt.SegmentOp) int {
	switch op {
	case sfnt.SegmentOpMoveTo, sfnt.SegmentOpLineTo:
		return 1
	case sfnt.SegmentOpQuadTo:
		return 2
	}
	return 3
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genicon:", err)
	os.Exit(1)
}
