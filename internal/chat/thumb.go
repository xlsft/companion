package chat

import (
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"

	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/pkg/contracts"
)

// thumbSide bounds the picture the chat keeps in memory; the stored blob
// stays model-sized.
const thumbSide = 320

// maxBlobSide guards against decoding a file that is not ours.
const maxBlobSide = 8192

// thumbOf returns a small copy of img, or nil for a nil img.
func thumbOf(img *image.RGBA) *image.RGBA {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	if w <= thumbSide && h <= thumbSide {
		cp := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(cp, cp.Bounds(), img, b.Min, draw.Src)
		return cp
	}
	scale := float64(thumbSide) / float64(max(w, h))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// thumbFromFile loads a stored screenshot as a thumbnail; nil when there is
// no blob or it cannot be read (the chat then shows only the chip).
func thumbFromFile(path string) *image.RGBA {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil || cfg.Width > maxBlobSide || cfg.Height > maxBlobSide {
		return nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil
	}
	img, err := png.Decode(f)
	if err != nil {
		return nil
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return thumbOf(rgba)
}

func metaOf(c history.Capture) contracts.CaptureMetadata {
	return contracts.CaptureMetadata{
		ID: c.ID, DisplayID: c.DisplayID, Native: c.Native, Model: c.Model, Bounds: c.Bounds,
		Scale: c.Scale, Rotation: c.Rotation, TopologyVersion: c.TopologyVersion, Timestamp: c.TakenAt,
	}
}
