package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
)

const iconSize = 32

// iconPNG draws the tray icon at runtime so no binary asset has to be kept
// in sync with the brand yet: a filled circle with a white core.
func iconPNG() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSize, iconSize))
	fill := color.NRGBA{R: 0x3b, G: 0x5b, B: 0xdb, A: 0xff}
	core := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	c := float64(iconSize-1) / 2
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			switch d := dx*dx + dy*dy; {
			case d <= 6*6:
				img.SetNRGBA(x, y, core)
			case d <= 15*15:
				img.SetNRGBA(x, y, fill)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // encoding an in-memory NRGBA cannot fail
	return buf.Bytes()
}

// iconICO wraps a PNG into a single-image .ico, which Windows requires for
// tray icons (PNG-in-ICO is supported since Vista).
func iconICO(pngData []byte) []byte {
	var buf bytes.Buffer
	le := binary.LittleEndian
	// ICONDIR: reserved, type=1 (icon), count=1.
	_ = binary.Write(&buf, le, [3]uint16{0, 1, 1})
	// ICONDIRENTRY: width, height, colours, reserved, planes, bpp, size, offset.
	buf.Write([]byte{iconSize, iconSize, 0, 0})
	_ = binary.Write(&buf, le, [2]uint16{1, 32})
	_ = binary.Write(&buf, le, [2]uint32{uint32(len(pngData)), 6 + 16})
	buf.Write(pngData)
	return buf.Bytes()
}
