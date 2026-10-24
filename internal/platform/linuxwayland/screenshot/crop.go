package screenshot

import (
	"fmt"
	"image"
	"math"

	"github.com/xlsft/openclick/pkg/contracts"
)

// sizeTolerance absorbs the rounding of fractional logical sizes when the
// screenshot's size is compared with the topology.
const sizeTolerance = 2.0

// Crop cuts area out of a whole-desktop screenshot. desktop is the union of
// all display bounds in logical coordinates, which is what the portal
// renders; the screenshot may be at a higher pixel density, so the factor
// between the two is returned as the crop's scale. A screenshot whose
// aspect does not match desktop was taken under another topology and is
// rejected with contracts.ErrStale. The crop is a copy, so the full desktop
// image can be dropped right away.
func Crop(img *image.RGBA, desktop, area contracts.Rect) (*image.RGBA, float64, error) {
	if desktop.Empty() || area.Empty() {
		return nil, 0, fmt.Errorf("screenshot: empty desktop or area")
	}
	b := img.Bounds()
	scale := float64(b.Dx()) / desktop.Width()
	if math.Abs(float64(b.Dy())-desktop.Height()*scale) > sizeTolerance {
		return nil, 0, fmt.Errorf("%w: screenshot %dx%d does not match desktop %gx%g",
			contracts.ErrStale, b.Dx(), b.Dy(), desktop.Width(), desktop.Height())
	}
	px := func(v, origin float64) int { return int(math.Round((v - origin) * scale)) }
	r := image.Rect(
		px(area.Min.X, desktop.Min.X), px(area.Min.Y, desktop.Min.Y),
		px(area.Max.X, desktop.Min.X), px(area.Max.Y, desktop.Min.Y),
	).Add(b.Min).Intersect(b)
	if r.Empty() {
		return nil, 0, fmt.Errorf("screenshot: area outside the desktop")
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		src := img.Pix[img.PixOffset(r.Min.X, r.Min.Y+y):]
		copy(out.Pix[y*out.Stride:y*out.Stride+r.Dx()*4], src[:r.Dx()*4])
	}
	return out, scale, nil
}
