package main

import (
	"image"
	"testing"
)

func TestTestFrame(t *testing.T) {
	tests := []struct {
		name  string
		w, h  int
		scale float64
	}{
		{"1x", 800, 600, 1},
		{"2x", 1600, 1200, 2},
		{"fractional", 1000, 750, 1.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, card := testFrame(tt.w, tt.h, tt.scale, float64(tt.w)/2, float64(tt.h)/2, "openclick")
			if img.Bounds() != image.Rect(0, 0, tt.w, tt.h) {
				t.Fatalf("bounds %v", img.Bounds())
			}
			if a := img.RGBAAt(0, 0).A; a != 0 {
				t.Fatalf("corner must stay transparent for click-through, alpha=%d", a)
			}
			if card.Empty() || !card.In(img.Bounds()) {
				t.Fatalf("card %v outside frame", card)
			}
			ringPx := img.RGBAAt(tt.w/2+int(60*tt.scale), tt.h/2)
			if ringPx.A == 0 {
				t.Fatal("ring not drawn")
			}
			// Premultiplied invariant: no channel exceeds alpha.
			for i := 0; i < len(img.Pix); i += 4 {
				a := img.Pix[i+3]
				if img.Pix[i] > a || img.Pix[i+1] > a || img.Pix[i+2] > a {
					t.Fatalf("pixel %d not premultiplied: %v", i/4, img.Pix[i:i+4])
				}
			}
		})
	}
}
