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
