package screenshot

import (
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/xlsft/companion/pkg/contracts"
)

// desktopImage paints each pixel with its own coordinates so a crop can be
// checked by its corners.
func desktopImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), A: 255})
		}
	}
	return img
}

func TestCrop(t *testing.T) {
	tests := []struct {
		name      string
		imgW      int
		imgH      int
		desktop   contracts.Rect
		area      contracts.Rect
		wantSize  image.Point
		wantScale float64
		wantFirst color.RGBA // top-left pixel of the crop
		wantErr   error
	}{
		{
			name: "single display, whole", imgW: 40, imgH: 20,
			desktop: contracts.RectXYWH(0, 0, 40, 20), area: contracts.RectXYWH(0, 0, 40, 20),
			wantSize: image.Pt(40, 20), wantScale: 1, wantFirst: color.RGBA{0, 0, 0, 255},
		},
		{
			name: "right display of two", imgW: 60, imgH: 20,
			desktop: contracts.RectXYWH(0, 0, 60, 20), area: contracts.RectXYWH(40, 0, 20, 10),
			wantSize: image.Pt(20, 10), wantScale: 1, wantFirst: color.RGBA{40, 0, 0, 255},
		},
		{
			name: "negative desktop origin", imgW: 60, imgH: 30,
			desktop: contracts.RectXYWH(-20, -10, 60, 30), area: contracts.RectXYWH(0, 0, 40, 20),
			wantSize: image.Pt(40, 20), wantScale: 1, wantFirst: color.RGBA{20, 10, 0, 255},
		},
		{
			name: "screenshot at 2x density", imgW: 80, imgH: 40,
			desktop: contracts.RectXYWH(0, 0, 40, 20), area: contracts.RectXYWH(10, 5, 20, 10),
			wantSize: image.Pt(40, 20), wantScale: 2, wantFirst: color.RGBA{20, 10, 0, 255},
		},
		{
			name: "area clipped to the image", imgW: 40, imgH: 20,
			desktop: contracts.RectXYWH(0, 0, 40, 20), area: contracts.RectXYWH(30, 10, 20, 20),
			wantSize: image.Pt(10, 10), wantScale: 1, wantFirst: color.RGBA{30, 10, 0, 255},
		},
		{
			name: "aspect mismatch is stale", imgW: 40, imgH: 40,
			desktop: contracts.RectXYWH(0, 0, 40, 20), area: contracts.RectXYWH(0, 0, 10, 10),
			wantErr: contracts.ErrStale,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, scale, err := Crop(desktopImage(tt.imgW, tt.imgH), tt.desktop, tt.area)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Bounds().Size() != tt.wantSize || scale != tt.wantScale {
				t.Fatalf("size %v scale %g, want %v %g", got.Bounds().Size(), scale, tt.wantSize, tt.wantScale)
			}
			if got.Bounds().Min != (image.Point{}) {
				t.Fatalf("crop must start at 0,0: %v", got.Bounds())
			}
			if c := got.RGBAAt(0, 0); c != tt.wantFirst {
				t.Fatalf("first pixel %v, want %v", c, tt.wantFirst)
			}
		})
	}
}

func TestCropOutside(t *testing.T) {
	_, _, err := Crop(desktopImage(10, 10), contracts.RectXYWH(0, 0, 10, 10), contracts.RectXYWH(20, 20, 5, 5))
	if err == nil {
		t.Fatal("expected error")
	}
}
