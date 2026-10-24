//go:build linux

// Package overlay implements contracts.OverlayHost on X11 (and Xwayland)
// with one override-redirect, 32-bit ARGB window per RandR monitor. An
// empty SHAPE input region makes it click-through; override-redirect keeps
// the window manager from focusing or decorating it. It uses the pure-Go
// xgb client, so no libX11 is needed at build time.
package overlay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/companion/internal/platform/pixconv"
	"github.com/xlsft/companion/pkg/contracts"
)

const Backend = "x11-shape-override-redirect"

// Host is one X connection shared by all its overlay windows.
type Host struct {
	c      *xgb.Conn
	screen *xproto.ScreenInfo
	visual xproto.Visualid
	cmap   xproto.Colormap

	atomState, atomAbove, atomType, atomTypeNotif, atomBypass xproto.Atom
}

// New connects to $DISPLAY. It returns contracts.Unsupported when the
// server lacks SHAPE or a 32-bit TrueColor visual (no compositing manager
// means the alpha channel is ignored; that is detected at runtime by the
// caller via _NET_WM_CM_S0, see Composited).
func New(ctx context.Context) (*Host, error) {
	xgb.Logger.SetOutput(nopWriter{})
	c, err := xgb.NewConn()
	if err != nil {
		return nil, &contracts.CapabilityError{Capability: contracts.CapOverlayClickThrough, Err: err}
	}
	if err := shape.Init(c); err != nil {
		c.Close()
		return nil, unsupported("no SHAPE extension")
	}
	h := &Host{c: c, screen: xproto.Setup(c).DefaultScreen(c)}
	for _, d := range h.screen.AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.Class == xproto.VisualClassTrueColor {
				h.visual = v.VisualId
				break
			}
		}
	}
	if h.visual == 0 {
		c.Close()
		return nil, unsupported("no 32-bit TrueColor visual")
	}
	h.cmap, err = xproto.NewColormapId(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	if err := xproto.CreateColormapChecked(c, xproto.ColormapAllocNone, h.cmap, h.screen.Root, h.visual).Check(); err != nil {
		c.Close()
		return nil, err
	}
	h.atomState = h.atom("_NET_WM_STATE")
	h.atomAbove = h.atom("_NET_WM_STATE_ABOVE")
	h.atomType = h.atom("_NET_WM_WINDOW_TYPE")
	h.atomTypeNotif = h.atom("_NET_WM_WINDOW_TYPE_NOTIFICATION")
	h.atomBypass = h.atom("_NET_WM_BYPASS_COMPOSITOR")
	return h, ctx.Err()
}

func unsupported(reason string) error {
	return &contracts.CapabilityError{
		Capability: contracts.CapOverlayClickThrough,
		Err:        fmt.Errorf("%w: %s", contracts.ErrUnsupported, reason),
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func (h *Host) atom(name string) xproto.Atom {
	r, err := xproto.InternAtom(h.c, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	return r.Atom
}

// Composited reports whether a compositing manager owns _NET_WM_CM_S0;
// without one, ARGB windows render opaque black.
func (h *Host) Composited() bool {
	a := h.atom("_NET_WM_CM_S0")
	if a == 0 {
		return false
	}
	r, err := xproto.GetSelectionOwner(h.c, a).Reply()
	return err == nil && r.Owner != 0
}

// FocusedWindow returns the X input focus, for the focus-steal check.
func (h *Host) FocusedWindow() (uint32, error) {
	r, err := xproto.GetInputFocus(h.c).Reply()
	if err != nil {
		return 0, err
	}
	return uint32(r.Focus), nil
}

// Displays lists RandR 1.5 monitors, or the whole root window when RandR
// is missing.
func (h *Host) Displays() []contracts.Display {
	root := contracts.Display{
		ID:     "screen",
		Name:   "screen",
		Bounds: contracts.RectXYWH(0, 0, float64(h.screen.WidthInPixels), float64(h.screen.HeightInPixels)),
		Native: contracts.PixelSize{W: int(h.screen.WidthInPixels), H: int(h.screen.HeightInPixels)},
		Scale:  1,
	}
	if err := randr.Init(h.c); err != nil {
		return []contracts.Display{root}
	}
	r, err := randr.GetMonitors(h.c, h.screen.Root, true).Reply()
	if err != nil || len(r.Monitors) == 0 {
		return []contracts.Display{root}
	}
	var out []contracts.Display
	for _, m := range r.Monitors {
		name := fmt.Sprintf("monitor-%d", len(out))
		if an, err := xproto.GetAtomName(h.c, m.Name).Reply(); err == nil {
			name = an.Name
		}
		out = append(out, contracts.Display{
			ID:      contracts.DisplayID(name), // RandR monitor name = output name, as in linuxx11/displays
			Name:    name,
			Bounds:  contracts.RectXYWH(float64(m.X), float64(m.Y), float64(m.Width), float64(m.Height)),
			Native:  contracts.PixelSize{W: int(m.Width), H: int(m.Height)},
			Scale:   1, // X11 has no per-monitor scale; Xft.dpi is global
			Primary: m.Primary,
		})
	}
	return out
}

func (h *Host) Open(ctx context.Context, display contracts.DisplayID) (contracts.OverlaySurface, error) {
	var d *contracts.Display
	for _, x := range h.Displays() {
		if x.ID == display {
			d = &x
			break
		}
	}
	if d == nil {
		return nil, fmt.Errorf("overlay: unknown display %q", display)
	}
	win, err := xproto.NewWindowId(h.c)
	if err != nil {
		return nil, err
	}
	x, y := int16(d.Bounds.Min.X), int16(d.Bounds.Min.Y)
	w, hh := uint16(d.Native.W), uint16(d.Native.H)
	// Value list order must follow the mask bit order.
	mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel | xproto.CwOverrideRedirect | xproto.CwColormap)
	vals := []uint32{0, 0, 1, uint32(h.cmap)}
	if err := xproto.CreateWindowChecked(h.c, 32, win, h.screen.Root, x, y, w, hh, 0,
		xproto.WindowClassInputOutput, h.visual, mask, vals).Check(); err != nil {
		return nil, err
	}
	s := &Surface{h: h, win: win, display: d.ID, w: int(w), hh: int(hh)}
	// Window-manager hints are ignored for override-redirect windows by
	// spec, but compositors (picom, KWin, Mutter) read them for stacking
	// and unredirection decisions.
	h.setAtoms(win, h.atomState, h.atomAbove)
	h.setAtoms(win, h.atomType, h.atomTypeNotif)
	h.setCardinal(win, h.atomBypass, 2) // 2 = never unredirect
	if err := s.SetInteractive(contracts.Rect{}); err != nil {
		s.Close()
		return nil, err
	}
	s.gc, err = xproto.NewGcontextId(h.c)
	if err != nil {
		s.Close()
		return nil, err
	}
	xproto.CreateGC(h.c, s.gc, xproto.Drawable(win), 0, nil)
	if err := xproto.MapWindowChecked(h.c, win).Check(); err != nil {
		s.Close()
		return nil, err
	}
	return s, ctx.Err()
}

func (h *Host) setAtoms(win xproto.Window, prop xproto.Atom, vals ...xproto.Atom) {
	if prop == 0 {
		return
	}
	b := make([]byte, 0, 4*len(vals))
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	xproto.ChangeProperty(h.c, xproto.PropModeReplace, win, prop, xproto.AtomAtom, 32, uint32(len(vals)), b)
}

func (h *Host) setCardinal(win xproto.Window, prop xproto.Atom, v uint32) {
	if prop == 0 {
		return
	}
	xproto.ChangeProperty(h.c, xproto.PropModeReplace, win, prop, xproto.AtomCardinal, 32, 1, binary.LittleEndian.AppendUint32(nil, v))
}

func (h *Host) Close() error {
	h.c.Close()
	return nil
}

// Surface is one ARGB override-redirect window.
type Surface struct {
	h       *Host
	win     xproto.Window
	gc      xproto.Gcontext
	display contracts.DisplayID
	w, hh   int

	mu          sync.Mutex
	closed      bool
	lastLatency time.Duration
	buf         []byte
}

func (s *Surface) Display() contracts.DisplayID { return s.display }

func (s *Surface) NativeSize() contracts.PixelSize { return contracts.PixelSize{W: s.w, H: s.hh} }

func (s *Surface) Scale() float64 { return 1 }

// LastFrameLatency is the time PutImage plus a server roundtrip took; X11
// gives no presentation feedback without the Present extension.
func (s *Surface) LastFrameLatency() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLatency
}

// maxRequestBytes keeps PutImage under the core 256 KiB request limit so
// we don't depend on BIG-REQUESTS.
const maxRequestBytes = 256*1024 - 64

func (s *Surface) Present(ctx context.Context, frame *image.RGBA) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("overlay: surface closed")
	}
	fw, fh := frame.Bounds().Dx(), frame.Bounds().Dy()
	if fw <= 0 || fh <= 0 || fw > math.MaxUint16 || fh > math.MaxUint16 {
		return errors.New("overlay: bad frame size")
	}
	stride := fw * 4
	if len(s.buf) != stride*fh {
		s.buf = make([]byte, stride*fh)
	}
	pixconv.RGBAToBGRA(s.buf, stride, frame)
	start := time.Now()
	rows := max(1, maxRequestBytes/stride)
	for y := 0; y < fh; y += rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(rows, fh-y)
		xproto.PutImage(s.h.c, xproto.ImageFormatZPixmap, xproto.Drawable(s.win), s.gc,
			uint16(fw), uint16(n), 0, int16(y), 0, 32, s.buf[y*stride:(y+n)*stride])
	}
	if _, err := xproto.GetInputFocus(s.h.c).Reply(); err != nil {
		return err
	}
	s.lastLatency = time.Since(start)
	return nil
}

// SetInteractive sets the SHAPE input region; an empty rect leaves no input
// area, so all pointer events go to the windows below.
func (s *Surface) SetInteractive(r contracts.Rect) error {
	var rects []xproto.Rectangle
	if !r.Empty() {
		x0, y0 := math.Floor(r.Min.X), math.Floor(r.Min.Y)
		rects = []xproto.Rectangle{{
			X: int16(x0), Y: int16(y0),
			Width: uint16(math.Ceil(r.Max.X) - x0), Height: uint16(math.Ceil(r.Max.Y) - y0),
		}}
	}
	return shape.RectanglesChecked(s.h.c, shape.SoSet, shape.SkInput, xproto.ClipOrderingUnsorted,
		s.win, 0, 0, rects).Check()
}

func (s *Surface) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.gc != 0 {
		xproto.FreeGC(s.h.c, s.gc)
	}
	return xproto.DestroyWindowChecked(s.h.c, s.win).Check()
}

var _ contracts.OverlaySurface = (*Surface)(nil)
var _ contracts.OverlayHost = (*Host)(nil)
