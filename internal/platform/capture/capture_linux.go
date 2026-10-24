//go:build linux

package capture

import (
	"context"
	"fmt"
	"image"
	"os"
	"strconv"
	"sync"

	"github.com/xlsft/companion/internal/platform/linuxwayland/screencopy"
	"github.com/xlsft/companion/internal/platform/linuxwayland/screenshot"
	x11capture "github.com/xlsft/companion/internal/platform/linuxx11/capture"
	"github.com/xlsft/companion/pkg/contracts"
)

// New opens the best capture backend for the session in report, using topo
// (normally the displays Tracker) for display geometry. It returns
// Unsupported(CapScreenCapture) when no backend works.
func New(ctx context.Context, report contracts.CapabilityReport, topo TopologySource) (*Capturer, error) {
	return choose(ctx, Candidates(report), openers(), topo, defaultActive())
}

// NewBackend opens one named backend, for diagnostics and the capture
// spike; it skips the capability report.
func NewBackend(ctx context.Context, name string, topo TopologySource) (*Capturer, error) {
	return choose(ctx, []string{name}, openers(), topo, defaultActive())
}

func defaultActive() []ActiveDisplay {
	if s := os.Getenv("NIRI_SOCKET"); s != "" {
		return []ActiveDisplay{NiriActive{Socket: s}}
	}
	return nil
}

func openers() map[string]opener {
	return map[string]opener{
		BackendScreencopy: func(ctx context.Context) (grabber, []ActiveDisplay, error) {
			b, err := screencopy.Open(ctx)
			if err != nil {
				return nil, nil, err
			}
			return screencopyGrabber{b}, nil, nil
		},
		BackendPortalScreenshot: func(context.Context) (grabber, []ActiveDisplay, error) {
			c, err := screenshot.Open()
			if err != nil {
				return nil, nil, err
			}
			return portalGrabber{c}, nil, nil
		},
		BackendX11: func(context.Context) (grabber, []ActiveDisplay, error) {
			b, err := x11capture.Open()
			if err != nil {
				return nil, nil, err
			}
			return x11Grabber{b}, []ActiveDisplay{ActiveFunc(func(_ context.Context, topo contracts.Topology) (contracts.DisplayID, error) {
				x, y, err := b.Pointer()
				if err != nil {
					return "", err
				}
				return displayAt(topo, contracts.Point{X: float64(x), Y: float64(y)})
			})}, nil
		},
	}
}

type screencopyGrabber struct{ b *screencopy.Backend }

func (g screencopyGrabber) close() error { return g.b.Close() }

// grab copies all targets concurrently; each is its own screencopy frame,
// so capturing every display costs about as much as one.
func (g screencopyGrabber) grab(ctx context.Context, _ contracts.Topology, ts []target) ([]shot, error) {
	shots := make([]shot, len(ts))
	errs := make([]error, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var region *image.Rectangle
			if !t.full {
				r := localRect(t)
				region = &r
			}
			f, err := g.b.Capture(ctx, string(t.display.ID), region)
			if err != nil {
				errs[i] = err
				return
			}
			b := f.Image.Bounds()
			shots[i] = shot{
				img:      f.Image,
				scale:    t.display.Scale,
				rotation: bufferRotation(t, b.Dx(), b.Dy()),
				at:       f.Ready,
				ids: map[string]string{
					"wl_output": string(t.display.ID),
					"format":    screencopy.FormatName(f.Format),
					"y_invert":  strconv.FormatBool(f.YInvert),
				},
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return shots, nil
}

type portalGrabber struct{ c *screenshot.Client }

func (g portalGrabber) close() error { return g.c.Close() }

// grab takes one whole-desktop screenshot and crops every target from it.
func (g portalGrabber) grab(ctx context.Context, topo contracts.Topology, ts []target) ([]shot, error) {
	full, err := g.c.Take(ctx)
	if err != nil {
		return nil, err
	}
	desktop := desktopBounds(topo)
	shots := make([]shot, len(ts))
	for i, t := range ts {
		img, scale, err := screenshot.Crop(full, desktop, t.area)
		if err != nil {
			return nil, err
		}
		shots[i] = shot{img: img, scale: scale, ids: map[string]string{
			"portal_version": strconv.FormatUint(uint64(g.c.Version), 10),
		}}
	}
	return shots, nil
}

type x11Grabber struct{ b *x11capture.Backend }

func (g x11Grabber) close() error { return g.b.Close() }

func (g x11Grabber) grab(_ context.Context, _ contracts.Topology, ts []target) ([]shot, error) {
	shots := make([]shot, len(ts))
	for i, t := range ts {
		r := deviceRect(t.area)
		img, err := g.b.Grab(r)
		if err != nil {
			return nil, err
		}
		shots[i] = shot{img: img, scale: 1, ids: map[string]string{
			"x11_output": string(t.display.ID),
			"root_rect":  fmt.Sprintf("%d,%d,%dx%d", r.Min.X, r.Min.Y, r.Dx(), r.Dy()),
		}}
	}
	return shots, nil
}
