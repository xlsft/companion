package screencopy

import (
	"bytes"
	"testing"
)

func TestConvert(t *testing.T) {
	// Two rows of two pixels; the second row is padded to stride 12.
	tests := []struct {
		name    string
		format  uint32
		src     []byte
		stride  int
		yInvert bool
		want    []byte // RGBA, 2x2
	}{
		{
			name:   "XRGB8888 is BGRx in memory",
			format: FormatXRGB8888, stride: 8,
			src:  []byte{3, 2, 1, 0, 6, 5, 4, 0, 9, 8, 7, 0, 12, 11, 10, 0},
			want: []byte{1, 2, 3, 255, 4, 5, 6, 255, 7, 8, 9, 255, 10, 11, 12, 255},
		},
		{
			name:   "ARGB8888 drops alpha",
			format: FormatARGB8888, stride: 8,
			src:  []byte{3, 2, 1, 0x10, 6, 5, 4, 0x20, 9, 8, 7, 0x30, 12, 11, 10, 0x40},
			want: []byte{1, 2, 3, 255, 4, 5, 6, 255, 7, 8, 9, 255, 10, 11, 12, 255},
		},
		{
			name:   "XBGR8888 is RGBx in memory, padded stride",
			format: FormatXBGR8888, stride: 12,
			src:  []byte{1, 2, 3, 0, 4, 5, 6, 0, 0xee, 0xee, 0xee, 0xee, 7, 8, 9, 0, 10, 11, 12, 0},
			want: []byte{1, 2, 3, 255, 4, 5, 6, 255, 7, 8, 9, 255, 10, 11, 12, 255},
		},
		{
			name:   "ABGR8888 y-inverted",
			format: FormatABGR8888, stride: 8, yInvert: true,
			src:  []byte{1, 2, 3, 9, 4, 5, 6, 9, 7, 8, 9, 9, 10, 11, 12, 9},
			want: []byte{7, 8, 9, 255, 10, 11, 12, 255, 1, 2, 3, 255, 4, 5, 6, 255},
		},
		{
			name:   "XRGB2101010 keeps the top 8 bits",
			format: FormatXRGB2101010, stride: 8,
			// R=0x3ff G=0x200 B=0x004 -> 0xff 0x80 0x01; then black, white, red.
			src:  le32(0x3ff<<20|0x200<<10|0x004, 0, 0x3fffffff, 0x3ff<<20),
			want: []byte{0xff, 0x80, 0x01, 255, 0, 0, 0, 255, 255, 255, 255, 255, 255, 0, 0, 255},
		},
		{
			name:   "XBGR2101010 has red in the low bits",
			format: FormatXBGR2101010, stride: 8,
			src:  le32(0x3ff, 0x3ff<<10, 0x3ff<<20, 0),
			want: []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 0, 0, 0, 255},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, err := Convert(tt.src, tt.format, 2, 2, tt.stride, tt.yInvert)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(img.Pix, tt.want) {
				t.Fatalf("got  %v\nwant %v", img.Pix, tt.want)
			}
		})
	}
}

func le32(vs ...uint32) []byte {
	var b []byte
	for _, v := range vs {
		b = append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	return b
}

func TestConvertRejects(t *testing.T) {
	tests := []struct {
		name          string
		format        uint32
		n, w, h, strd int
	}{
		{"short buffer", FormatXRGB8888, 12, 2, 2, 8},
		{"stride too small", FormatXRGB8888, 64, 2, 2, 4},
		{"zero size", FormatXRGB8888, 64, 0, 2, 8},
		{"unknown format", 'R' | 'G'<<8 | '2'<<16 | '4'<<24, 64, 2, 2, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Convert(make([]byte, tt.n), tt.format, tt.w, tt.h, tt.strd, false); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestYInvertOddHeight(t *testing.T) {
	src := []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0} // 1x3 XBGR
	img, err := Convert(src, FormatXBGR8888, 1, 3, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := []byte{img.Pix[0], img.Pix[4], img.Pix[8]}; !bytes.Equal(got, []byte{3, 2, 1}) {
		t.Fatalf("rows = %v", got)
	}
}

func TestFormatName(t *testing.T) {
	for f, want := range map[uint32]string{
		FormatXRGB8888: "XRGB8888", FormatARGB8888: "ARGB8888", FormatXBGR8888: "XB24", 0x01020304: "0x01020304",
	} {
		if got := FormatName(f); got != want {
			t.Errorf("FormatName(%#x) = %q, want %q", f, got, want)
		}
	}
}
