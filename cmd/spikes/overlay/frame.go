package main

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var (
	accent = color.RGBA{0x4f, 0x8c, 0xff, 0xff}
	panel  = color.RGBA{0x10, 0x14, 0x1c, 0xd8}
	white  = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// testFrame draws the OC-5 test pattern into a transparent frame of the
// given native size: a target ring at (cx, cy), an arrow pointing at it
// and a label card. It returns the label card bounds in native pixels so
// the caller can make exactly that area interactive.
func testFrame(w, h int, scale float64, cx, cy float64, text string) (*image.RGBA, image.Rectangle) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := 60 * scale
	ring(img, cx, cy, r, 6*scale, accent)
	ring(img, cx, cy, r+10*scale, 2*scale, color.RGBA{0x4f, 0x8c, 0xff, 0x60})

	// Arrow from upper-left toward the ring edge.
	ax, ay := cx-260*scale, cy-170*scale
	dx, dy := cx-ax, cy-ay
	l := math.Hypot(dx, dy)
	ux, uy := dx/l, dy/l
	tipX, tipY := cx-ux*(r+14*scale), cy-uy*(r+14*scale)
	head := 26 * scale
	baseX, baseY := tipX-ux*head, tipY-uy*head
	line(img, ax, ay, baseX, baseY, 5*scale, accent)
	px, py := -uy, ux
	triangle(img,
		tipX, tipY,
		baseX+px*head*0.55, baseY+py*head*0.55,
		baseX-px*head*0.55, baseY-py*head*0.55, accent)

	// Label card to the right of the ring.
	fs := max(1, int(math.Round(2*scale)))
	face := basicfont.Face7x13
	tw := font.MeasureString(face, text).Ceil() * fs
	th := 13 * fs
	pad := int(12 * scale)
	x0 := int(cx + r + 24*scale)
	y0 := int(cy) - th/2 - pad
	card := image.Rect(x0, y0, x0+tw+2*pad, y0+th+2*pad)
	roundRect(img, card, 8*scale, panel)
	drawText(img, x0+pad, y0+pad, fs, text, white)
	return img, card
}

// over composites premultiplied c with coverage a onto pixel (x, y).
func over(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	if !(image.Point{x, y}.In(img.Rect)) || a <= 0 {
		return
	}
	a = min(a, 1)
	i := img.PixOffset(x, y)
	sa := float64(c.A) / 255 * a
	// c is straight alpha; premultiply on the way in.
	sr, sg, sb := float64(c.R)*sa, float64(c.G)*sa, float64(c.B)*sa
	p := img.Pix[i : i+4 : i+4]
	k := 1 - sa
	p[0] = uint8(sr + float64(p[0])*k)
	p[1] = uint8(sg + float64(p[1])*k)
	p[2] = uint8(sb + float64(p[2])*k)
	p[3] = uint8(sa*255 + float64(p[3])*k)
}

func ring(img *image.RGBA, cx, cy, r, t float64, c color.RGBA) {
	ext := int(r + t + 2)
	for y := int(cy) - ext; y <= int(cy)+ext; y++ {
		for x := int(cx) - ext; x <= int(cx)+ext; x++ {
			d := math.Abs(math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) - r)
			over(img, x, y, c, t/2+0.5-d)
		}
	}
}

func line(img *image.RGBA, x0, y0, x1, y1, t float64, c color.RGBA) {
	minX, maxX := int(min(x0, x1)-t), int(max(x0, x1)+t)
	minY, maxY := int(min(y0, y1)-t), int(max(y0, y1)+t)
	dx, dy := x1-x0, y1-y0
	ll := dx*dx + dy*dy
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			u := max(0, min(1, ((px-x0)*dx+(py-y0)*dy)/ll))
			d := math.Hypot(px-(x0+u*dx), py-(y0+u*dy))
			over(img, x, y, c, t/2+0.5-d)
		}
	}
}

func triangle(img *image.RGBA, ax, ay, bx, by, cx, cy float64, c color.RGBA) {
	minX, maxX := int(min(ax, bx, cx)), int(max(ax, bx, cx))+1
	minY, maxY := int(min(ay, by, cy)), int(max(ay, by, cy))+1
	edge := func(x0, y0, x1, y1, px, py float64) float64 { return (x1-x0)*(py-y0) - (y1-y0)*(px-x0) }
	area := edge(ax, ay, bx, by, cx, cy)
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			w0 := edge(bx, by, cx, cy, px, py) / area
			w1 := edge(cx, cy, ax, ay, px, py) / area
			w2 := edge(ax, ay, bx, by, px, py) / area
			if w0 >= 0 && w1 >= 0 && w2 >= 0 {
				over(img, x, y, c, 1)
			}
		}
	}
}

func roundRect(img *image.RGBA, r image.Rectangle, rad float64, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			qx := max(float64(r.Min.X)+rad-px, 0, px-(float64(r.Max.X)-rad))
			qy := max(float64(r.Min.Y)+rad-py, 0, py-(float64(r.Max.Y)-rad))
			d := math.Hypot(qx, qy) - rad
			over(img, x, y, c, 0.5-d)
		}
	}
}

// drawText renders with the 7x13 bitmap font scaled by an integer factor,
// enough for a spike without pulling in a TrueType rasterizer.
func drawText(img *image.RGBA, x, y, scale int, text string, c color.RGBA) {
	face := basicfont.Face7x13
	mask := image.NewAlpha(image.Rect(0, 0, font.MeasureString(face, text).Ceil(), 13))
	d := font.Drawer{Dst: mask, Src: image.Opaque, Face: face, Dot: fixed.P(0, face.Ascent)}
	d.DrawString(text)
	for my := 0; my < mask.Rect.Dy(); my++ {
		for mx := 0; mx < mask.Rect.Dx(); mx++ {
			a := float64(mask.AlphaAt(mx, my).A) / 255
			if a == 0 {
				continue
			}
			for sy := 0; sy < scale; sy++ {
				for sx := 0; sx < scale; sx++ {
					over(img, x+mx*scale+sx, y+my*scale+sy, c, a)
				}
			}
		}
	}
}
