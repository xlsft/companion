package screencopy

import (
	"errors"
	"fmt"
	"image"

	"github.com/xlsft/openclick/internal/platform/pixconv"
)

// wl_shm formats this package can convert. ARGB8888 and XRGB8888 have the
// special codes 0 and 1; all others are DRM fourcc codes. Names are
// little-endian channel order, so XRGB8888 is B,G,R,X in memory.
const (
	FormatARGB8888    uint32 = 0
	FormatXRGB8888    uint32 = 1
	FormatXBGR8888    uint32 = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	FormatABGR8888    uint32 = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
	FormatXRGB2101010 uint32 = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
	FormatARGB2101010 uint32 = 'A' | 'R'<<8 | '3'<<16 | '0'<<24
	FormatXBGR2101010 uint32 = 'X' | 'B'<<8 | '3'<<16 | '0'<<24
	FormatABGR2101010 uint32 = 'A' | 'B'<<8 | '3'<<16 | '0'<<24
)

// Supported reports whether Convert handles format.
func Supported(format uint32) bool {
	switch format {
	case FormatARGB8888, FormatXRGB8888, FormatXBGR8888, FormatABGR8888,
		FormatXRGB2101010, FormatARGB2101010, FormatXBGR2101010, FormatABGR2101010:
		return true
	}
	return false
}

// FormatName is a short diagnostic label for a wl_shm format.
func FormatName(format uint32) string {
	switch format {
	case FormatARGB8888:
		return "ARGB8888"
	case FormatXRGB8888:
		return "XRGB8888"
	}
	b := []byte{byte(format), byte(format >> 8), byte(format >> 16), byte(format >> 24)}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("0x%08x", format)
		}
	}
	return string(b)
}

var errShortBuffer = errors.New("screencopy: buffer smaller than stride*height")

// Convert turns a captured wl_shm buffer into an opaque image.RGBA. Alpha
// is dropped because what an output shows is opaque whatever the buffer's
// alpha channel says. yInvert flips rows, as the frame's flags event asks.
func Convert(src []byte, format uint32, w, h, stride int, yInvert bool) (*image.RGBA, error) {
	if w <= 0 || h <= 0 || stride < w*4 {
		return nil, fmt.Errorf("screencopy: bad buffer geometry %dx%d stride %d", w, h, stride)
	}
	if len(src) < stride*(h-1)+w*4 {
		return nil, errShortBuffer
	}
	var img *image.RGBA
	switch format {
	case FormatARGB8888, FormatXRGB8888:
		img = pixconv.BGRxToRGBA(src, stride, w, h, true)
	case FormatXBGR8888, FormatABGR8888:
		img = pixconv.BGRxToRGBA(src, stride, w, h, false)
	case FormatXRGB2101010, FormatARGB2101010:
		img = convert2101010(src, stride, w, h, false)
	case FormatXBGR2101010, FormatABGR2101010:
		img = convert2101010(src, stride, w, h, true)
	default:
		return nil, fmt.Errorf("screencopy: unsupported format %s", FormatName(format))
	}
	if yInvert {
		flipRows(img)
	}
	return img, nil
}

// convert2101010 handles the 10-bit formats HDR-capable compositors may
// prefer; each channel keeps its top 8 bits.
func convert2101010(src []byte, stride, w, h int, bgr bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		s := src[y*stride : y*stride+w*4]
		d := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for i := 0; i < len(s); i += 4 {
			v := uint32(s[i]) | uint32(s[i+1])<<8 | uint32(s[i+2])<<16 | uint32(s[i+3])<<24
			hi, mid, lo := byte(v>>22), byte(v>>12), byte(v>>2) // bits 29-20, 19-10, 9-0
			if bgr {
				d[i], d[i+1], d[i+2] = lo, mid, hi
			} else {
				d[i], d[i+1], d[i+2] = hi, mid, lo
			}
			d[i+3] = 0xff
		}
	}
	return img
}

func flipRows(img *image.RGBA) {
	h := img.Bounds().Dy()
	n := img.Bounds().Dx() * 4
	tmp := make([]byte, n)
	for y := 0; y < h/2; y++ {
		a := img.Pix[y*img.Stride : y*img.Stride+n]
		b := img.Pix[(h-1-y)*img.Stride : (h-1-y)*img.Stride+n]
		copy(tmp, a)
		copy(a, b)
		copy(b, tmp)
	}
}
