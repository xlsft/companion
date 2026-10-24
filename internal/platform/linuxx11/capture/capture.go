// Package capture grabs X11 screen areas with GetImage on the root window
// (pure Go over xgb). The root window spans every CRTC in one device-pixel
// space, so captures are always at scale 1 and already in screen
// orientation.
package capture

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/companion/pkg/contracts"
)

// Backend owns one X connection.
type Backend struct {
	conn *xgb.Conn
	root xproto.Window
	// lsbFirst is the server's image byte order; with 32 bits per pixel
	// and depth 24 LSBFirst is B,G,R,x in memory.
	lsbFirst bool

	closeOnce sync.Once
}

// Open connects to $DISPLAY and checks that the root visual is a 24/32-bit
// TrueColor layout this package can convert.
func Open() (*Backend, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11: connect: %w", err)
	}
	setup := xproto.Setup(conn)
	scr := setup.DefaultScreen(conn)
	if err := checkFormat(setup, scr); err != nil {
		conn.Close()
		return nil, err
	}
	b := &Backend{conn: conn, root: scr.Root, lsbFirst: setup.ImageByteOrder == xproto.ImageOrderLSBFirst}
	// Rootless Xwayland (xwayland-satellite, XWayland under GNOME/KDE) has
	// no root contents and answers GetImage with BadMatch; find out now so
	// the chooser can move on instead of failing every capture.
	if _, err := b.Grab(image.Rect(0, 0, 1, 1)); err != nil {
		conn.Close()
		if _, ok := errors.AsType[xproto.MatchError](err); ok {
			return nil, &contracts.CapabilityError{
				Capability: contracts.CapScreenCapture,
				Err:        fmt.Errorf("%w: root window not readable (rootless Xwayland?)", contracts.ErrUnsupported),
			}
		}
		return nil, err
	}
	return b, nil
}

func checkFormat(setup *xproto.SetupInfo, scr *xproto.ScreenInfo) error {
	if scr.RootDepth != 24 && scr.RootDepth != 32 {
		return fmt.Errorf("x11: root depth %d not supported", scr.RootDepth)
	}
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			if f.BitsPerPixel != 32 {
				return fmt.Errorf("x11: %d bits per pixel not supported", f.BitsPerPixel)
			}
			return nil
		}
	}
	return errors.New("x11: no pixmap format for root depth")
}

func (b *Backend) Close() error {
	b.closeOnce.Do(b.conn.Close)
	return nil
}

// Grab returns the root-window area r (device pixels) as opaque RGBA.
func (b *Backend) Grab(r image.Rectangle) (*image.RGBA, error) {
	if r.Empty() || r.Dx() > 0xffff || r.Dy() > 0xffff {
		return nil, fmt.Errorf("x11: bad area %v", r)
	}
	rep, err := xproto.GetImage(b.conn, xproto.ImageFormatZPixmap, xproto.Drawable(b.root),
		int16(r.Min.X), int16(r.Min.Y), uint16(r.Dx()), uint16(r.Dy()), 0xffffffff).Reply()
	if err != nil {
		return nil, fmt.Errorf("x11: GetImage: %w", err)
	}
	return Convert(rep.Data, r.Dx(), r.Dy(), b.lsbFirst)
}

// Pointer returns the pointer position on the root window.
func (b *Backend) Pointer() (x, y int, err error) {
	rep, err := xproto.QueryPointer(b.conn, b.root).Reply()
	if err != nil {
		return 0, 0, fmt.Errorf("x11: QueryPointer: %w", err)
	}
	if !rep.SameScreen {
		return 0, 0, errors.New("x11: pointer on another screen")
	}
	return int(rep.RootX), int(rep.RootY), nil
}

// Convert turns a 32-bpp ZPixmap (scanlines padded to 32 bits, so the
// stride is w*4) into opaque RGBA.
func Convert(data []byte, w, h int, lsbFirst bool) (*image.RGBA, error) {
	if len(data) < w*h*4 {
		return nil, fmt.Errorf("x11: image data %d bytes, want %d", len(data), w*h*4)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	s, d := data[:w*h*4], img.Pix
	if lsbFirst { // B,G,R,x
		for i := 0; i < len(s); i += 4 {
			d[i], d[i+1], d[i+2], d[i+3] = s[i+2], s[i+1], s[i], 0xff
		}
	} else { // x,R,G,B
		for i := 0; i < len(s); i += 4 {
			d[i], d[i+1], d[i+2], d[i+3] = s[i+1], s[i+2], s[i+3], 0xff
		}
	}
	return img, nil
}
