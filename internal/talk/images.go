package talk

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"golang.org/x/image/draw"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/pkg/contracts"
)

// prepareCaptures downscales each capture for the model, records the model
// size in its metadata for the CoordinateMapper, registers it with the
// overlay and encodes it. The first capture is the one tools draw on.
func (o *Orchestrator) prepareCaptures(caps []*contracts.Capture) ([]llm.Image, contracts.CaptureID, error) {
	maxSide := o.MaxModelSide
	if maxSide <= 0 {
		maxSide = defaultMaxModelSide
	}
	var (
		images  []llm.Image
		primary contracts.CaptureID
	)
	for _, c := range caps {
		if c == nil || c.Image == nil {
			continue
		}
		scaled := downscale(c.Image, maxSide)
		meta := c.Meta
		meta.Model = contracts.PixelSize{W: scaled.Bounds().Dx(), H: scaled.Bounds().Dy()}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, scaled); err != nil {
			return nil, "", fmt.Errorf("encode capture: %w", err)
		}
		images = append(images, llm.Image{MediaType: llm.MediaPNG, Data: buf.Bytes()})
		if o.Overlay != nil {
			o.Overlay.RegisterCapture(meta)
		}
		if primary == "" {
			primary = meta.ID
		}
	}
	return images, primary, nil
}

// downscale fits img into maxSide×maxSide keeping the aspect ratio. The
// model image must be a uniform resize of the whole capture so normalized
// coordinates map back exactly.
func downscale(img *image.RGBA, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return img
	}
	scale := float64(maxSide) / float64(max(w, h))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
