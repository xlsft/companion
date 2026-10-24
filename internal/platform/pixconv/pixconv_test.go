package pixconv

import (
	"bytes"
	"image"
	"testing"
)

func TestRGBAToBGRA(t *testing.T) {
	tests := []struct {
		name      string
		pix       []byte
		dstStride int
		want      []byte
	}{
		{"one pixel", []byte{1, 2, 3, 4}, 4, []byte{3, 2, 1, 4}},
		{"padded stride", []byte{1, 2, 3, 4}, 8, []byte{3, 2, 1, 4, 0, 0, 0, 0}},
		{"transparent", []byte{0, 0, 0, 0}, 4, []byte{0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := image.NewRGBA(image.Rect(0, 0, 1, 1))
			copy(src.Pix, tt.pix)
			dst := make([]byte, tt.dstStride)
			RGBAToBGRA(dst, tt.dstStride, src)
			if !bytes.Equal(dst, tt.want) {
				t.Fatalf("got %v want %v", dst, tt.want)
			}
		})
	}
}

func TestBGRxToRGBA(t *testing.T) {
	tests := []struct {
		name string
		swap bool
		want []byte
	}{
		{"bgrx", true, []byte{30, 20, 10, 255}},
		{"rgbx", false, []byte{10, 20, 30, 255}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := BGRxToRGBA([]byte{10, 20, 30, 0}, 4, 1, 1, tt.swap)
			if !bytes.Equal(img.Pix, tt.want) {
				t.Fatalf("got %v want %v", img.Pix, tt.want)
			}
		})
	}
}

func TestRGBAToBGRARect(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range src.Pix {
		src.Pix[i] = byte(i + 1)
	}
	tests := []struct {
		name string
		r    image.Rectangle
		// changed lists the pixel indexes (y*3+x) that must be written.
		changed []int
	}{
		{"middle column", image.Rect(1, 0, 2, 2), []int{1, 4}},
		{"clipped to frame", image.Rect(2, 1, 9, 9), []int{5}},
		{"empty", image.Rectangle{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			full := make([]byte, 3*2*4)
			RGBAToBGRA(full, 12, src)
			dst := make([]byte, len(full))
			RGBAToBGRARect(dst, 12, src, tt.r)
			for px := range 6 {
				want := make([]byte, 4)
				for _, c := range tt.changed {
					if c == px {
						want = full[px*4 : px*4+4]
					}
				}
				if !bytes.Equal(dst[px*4:px*4+4], want) {
					t.Errorf("pixel %d = %v, want %v", px, dst[px*4:px*4+4], want)
				}
			}
		})
	}
}
