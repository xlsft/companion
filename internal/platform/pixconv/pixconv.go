// Package pixconv converts Go images to the byte layouts native overlay and
// capture backends exchange with the OS.
package pixconv

import "image"

// RGBAToBGRA writes src into dst as premultiplied 32-bit little-endian
// ARGB (bytes B,G,R,A), the layout of wl_shm ARGB8888, X11 depth-32
// ZPixmap and Windows premultiplied DIBs. image.RGBA is already
// premultiplied, so only the channel order changes. dst must hold
// dstStride*src.Bounds().Dy() bytes.
func RGBAToBGRA(dst []byte, dstStride int, src *image.RGBA) {
	b := src.Bounds()
	w := b.Dx()
	for y := 0; y < b.Dy(); y++ {
		s := src.Pix[y*src.Stride : y*src.Stride+w*4]
		d := dst[y*dstStride : y*dstStride+w*4]
		for i := 0; i < len(s); i += 4 {
			d[i+0] = s[i+2]
			d[i+1] = s[i+1]
			d[i+2] = s[i+0]
			d[i+3] = s[i+3]
		}
	}
}

// RGBAToBGRARect is RGBAToBGRA for the part r of src only; dst has the
// layout of the whole frame, origin at src.Bounds().Min.
func RGBAToBGRARect(dst []byte, dstStride int, src *image.RGBA, r image.Rectangle) {
	b := src.Bounds()
	r = r.Intersect(b)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		so := src.PixOffset(r.Min.X, y)
		s := src.Pix[so : so+r.Dx()*4]
		do := (y-b.Min.Y)*dstStride + (r.Min.X-b.Min.X)*4
		d := dst[do : do+r.Dx()*4]
		for i := 0; i < len(s); i += 4 {
			d[i+0] = s[i+2]
			d[i+1] = s[i+1]
			d[i+2] = s[i+0]
			d[i+3] = s[i+3]
		}
	}
}

// BGRxToRGBA converts a 32-bit buffer to an opaque image.RGBA, as delivered
// by PipeWire screen-cast streams. swapRB is true for B,G,R,x byte order
// and false for R,G,B,x.
func BGRxToRGBA(src []byte, srcStride, w, h int, swapRB bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		s := src[y*srcStride : y*srcStride+w*4]
		d := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for i := 0; i < len(s); i += 4 {
			if swapRB {
				d[i+0], d[i+2] = s[i+2], s[i+0]
			} else {
				d[i+0], d[i+2] = s[i+0], s[i+2]
			}
			d[i+1] = s[i+1]
			d[i+3] = 0xff
		}
	}
	return img
}
