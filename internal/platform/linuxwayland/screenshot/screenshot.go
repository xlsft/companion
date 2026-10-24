//go:build linux

// Package screenshot captures the whole desktop through the
// xdg-desktop-portal Screenshot interface with interactive=false. It is the
// Wayland fallback where zwlr_screencopy is missing (GNOME, KDE): no
// selection is possible, so callers crop per display with Crop.
package screenshot

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/url"
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/internal/platform/linuxwayland/portal"
	"github.com/xlsft/companion/pkg/contracts"
)

// Client holds a session-bus connection to the portal.
type Client struct {
	c       *portal.Conn
	Version uint32
}

// Open connects to the portal. It returns Unsupported(CapScreenCapture)
// when the Screenshot interface is not exported.
func Open() (*Client, error) {
	c, err := portal.Connect()
	if err != nil {
		return nil, &contracts.CapabilityError{Capability: contracts.CapScreenCapture, Err: err}
	}
	v := c.Version(portal.IfScreenshot)
	if v == 0 {
		c.Close()
		return nil, &contracts.CapabilityError{
			Capability: contracts.CapScreenCapture,
			Err:        fmt.Errorf("%w: portal Screenshot absent", contracts.ErrUnsupported),
		}
	}
	return &Client{c: c, Version: v}, nil
}

func (cl *Client) Close() error { return cl.c.Close() }

// Take returns the whole desktop. The portal backend writes a PNG file; it
// is unlinked as soon as it is open so screen content never lingers on disk
// (FR-SCREEN-006).
func (cl *Client) Take(ctx context.Context) (*image.RGBA, error) {
	res, err := cl.c.Request(ctx, portal.IfScreenshot+".Screenshot",
		map[string]dbus.Variant{"interactive": dbus.MakeVariant(false)}, "")
	switch {
	case errors.Is(err, portal.ErrCancelled):
		return nil, contracts.PermissionDenied(contracts.CapScreenCapture)
	case err != nil:
		return nil, err
	}
	uri, _ := res["uri"].Value().(string)
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return nil, errors.New("screenshot: portal returned no file uri")
	}
	f, err := os.Open(u.Path)
	if err != nil {
		_ = os.Remove(u.Path)
		return nil, fmt.Errorf("screenshot: open result: %w", err)
	}
	_ = os.Remove(u.Path)
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("screenshot: decode: %w", err)
	}
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba, nil
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	return out, nil
}
