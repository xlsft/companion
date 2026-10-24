package talk

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

	"golang.org/x/image/draw"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/pkg/contracts"
)

// prepareCaptures downscales each capture for the model, records the model
// size in its metadata for the CoordinateMapper, registers it with the
// overlay and encodes it. The first capture is the one tools draw on. The
// chat is told what was attached; it gets pixels only if the user opted in.
func (o *Orchestrator) prepareCaptures(ref contracts.SessionRef, caps []*contracts.Capture) ([]llm.Image, contracts.CaptureID, error) {
	return o.prepare(ref, caps, true)
}

// prepare is prepareCaptures; notify false keeps the chat out of it.
func (o *Orchestrator) prepare(ref contracts.SessionRef, caps []*contracts.Capture, notify bool) ([]llm.Image, contracts.CaptureID, error) {
	maxSide := o.MaxModelSide
	if maxSide <= 0 {
		maxSide = defaultMaxModelSide
	}
	var (
		images  []llm.Image
		primary contracts.CaptureID
		shots   []contracts.ScreenAttachment
		keep    = notify && o.KeepScreenshots != nil && o.KeepScreenshots()
	)
	for _, c := range caps {
		if c == nil || c.Image == nil {
			continue
		}
		img, scaled, meta, err := modelImage(c, maxSide)
		if err != nil {
			return nil, "", err
		}
		shot := contracts.ScreenAttachment{Meta: meta}
		if keep {
			// A copy: the capture's own buffer is zeroed right after.
			cp := image.NewRGBA(scaled.Bounds())
			draw.Draw(cp, cp.Bounds(), scaled, scaled.Bounds().Min, draw.Src)
			shot.Image = cp
		}
		shots = append(shots, shot)
		images = append(images, img)
		if o.Overlay != nil {
			o.Overlay.RegisterCapture(meta)
		}
		if primary == "" {
			primary = meta.ID
		}
	}
	if notify && o.Attachments != nil && len(shots) > 0 {
		o.Attachments.ScreenAttached(ref, shots)
	}
	return images, primary, nil
}

// ModelImage downscales c to fit maxSide×maxSide and encodes it as PNG
// for a model request. The returned metadata has Model set to the sent
// size, which the CoordinateMapper needs to map points back.
func ModelImage(c *contracts.Capture, maxSide int) (llm.Image, contracts.CaptureMetadata, error) {
	img, _, meta, err := modelImage(c, maxSide)
	return img, meta, err
}

// modelImage also returns the scaled pixels, which may alias c.Image.
func modelImage(c *contracts.Capture, maxSide int) (llm.Image, image.Image, contracts.CaptureMetadata, error) {
	scaled := downscale(c.Image, maxSide)
	meta := c.Meta
	meta.Model = contracts.PixelSize{W: scaled.Bounds().Dx(), H: scaled.Bounds().Dy()}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, scaled); err != nil {
		return llm.Image{}, nil, meta, fmt.Errorf("encode capture: %w", err)
	}
	return llm.Image{MediaType: llm.MediaPNG, Data: buf.Bytes()}, scaled, meta, nil
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
