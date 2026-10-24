// Package displays reads the display topology from an X server through
// XRandR (pure Go, no cgo) and reports hotplug and mode changes.
package displays

import (
	"bufio"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Backend implements internal/displays.Backend for X11.
type Backend struct {
	conn *xgb.Conn
	root xproto.Window

	closeOnce sync.Once
	done      chan struct{}
	changed   chan struct{}
}

// Open connects to the server named by $DISPLAY and subscribes to RandR
// screen, CRTC and output change notifications.
func Open() (*Backend, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11: connect: %w", err)
	}
	if err := randr.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("x11: RandR unavailable: %w", err)
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	mask := uint16(randr.NotifyMaskScreenChange | randr.NotifyMaskCrtcChange | randr.NotifyMaskOutputChange)
	if err := randr.SelectInputChecked(conn, root, mask).Check(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("x11: select RandR events: %w", err)
	}
	b := &Backend{conn: conn, root: root, done: make(chan struct{}), changed: make(chan struct{}, 1)}
	go b.loop()
	return b, nil
}

func (b *Backend) loop() {
	defer close(b.done)
	defer close(b.changed)
	for {
		ev, err := b.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed
		}
		if err != nil {
			continue // protocol error for a request we already handled
		}
		select {
		case b.changed <- struct{}{}:
		default:
		}
	}
}

func (b *Backend) Changed() <-chan struct{} { return b.changed }

func (b *Backend) Close() error {
	b.closeOnce.Do(b.conn.Close)
	<-b.done
	return nil
}

// XftDPI returns Xft.dpi from the root's RESOURCE_MANAGER, or 0 if unset.
// X11 has no per-output scale, so this is reported as a fact only and never
// applied to Display.Scale.
func (b *Backend) XftDPI() float64 {
	atom, err := xproto.InternAtom(b.conn, true, uint16(len("RESOURCE_MANAGER")), "RESOURCE_MANAGER").Reply()
	if err != nil || atom.Atom == 0 {
		return 0
	}
	rep, err := xproto.GetProperty(b.conn, false, b.root, atom.Atom, xproto.AtomString, 0, 1<<16).Reply()
	if err != nil {
		return 0
	}
	return parseXftDPI(string(rep.Value))
}

type outputInfo struct {
	name string
	crtc randr.Crtc
	conn byte
	id   randr.Output
}

type crtcInfo struct {
	x, y          int
	width, height int
	rotation      uint16
}

// Displays queries the server; X11 gives no cheap incremental state, and
// topology changes are rare.
func (b *Backend) Displays() ([]contracts.Display, error) {
	res, err := randr.GetScreenResourcesCurrent(b.conn, b.root).Reply()
	if err != nil {
		return nil, fmt.Errorf("x11: screen resources: %w", err)
	}
	crtcCookies := make(map[randr.Crtc]randr.GetCrtcInfoCookie, len(res.Crtcs))
	for _, c := range res.Crtcs {
		crtcCookies[c] = randr.GetCrtcInfo(b.conn, c, res.ConfigTimestamp)
	}
	outCookies := make([]randr.GetOutputInfoCookie, len(res.Outputs))
	for i, o := range res.Outputs {
		outCookies[i] = randr.GetOutputInfo(b.conn, o, res.ConfigTimestamp)
	}
	primary, _ := randr.GetOutputPrimary(b.conn, b.root).Reply()

	crtcs := map[randr.Crtc]crtcInfo{}
	for c, ck := range crtcCookies {
		r, err := ck.Reply()
		if err != nil {
			return nil, fmt.Errorf("x11: crtc info: %w", err)
		}
		crtcs[c] = crtcInfo{int(r.X), int(r.Y), int(r.Width), int(r.Height), r.Rotation}
	}
	var outs []outputInfo
	for i, ck := range outCookies {
		r, err := ck.Reply()
		if err != nil {
			return nil, fmt.Errorf("x11: output info: %w", err)
		}
		outs = append(outs, outputInfo{name: string(r.Name), crtc: r.Crtc, conn: r.Connection, id: res.Outputs[i]})
	}
	var primaryOut randr.Output
	if primary != nil {
		primaryOut = primary.Output
	}
	return buildDisplays(outs, crtcs, primaryOut), nil
}

// buildDisplays turns XRandR state into displays. Only connected outputs
// that a CRTC drives are active; disabled ones are not part of the desktop.
func buildDisplays(outs []outputInfo, crtcs map[randr.Crtc]crtcInfo, primary randr.Output) []contracts.Display {
	var ds []contracts.Display
	var primaryName string
	for _, o := range outs {
		c, ok := crtcs[o.crtc]
		if o.conn != randr.ConnectionConnected || o.crtc == 0 || !ok || c.width <= 0 || c.height <= 0 {
			continue
		}
		if primary != 0 && o.id == primary {
			primaryName = o.name
		}
		ds = append(ds, contracts.Display{
			ID:   contracts.DisplayID(o.name),
			Name: o.name,
			// X11 has one coordinate space in device pixels.
			Bounds:   contracts.RectXYWH(float64(c.x), float64(c.y), float64(c.width), float64(c.height)),
			Native:   contracts.PixelSize{W: c.width, H: c.height},
			Scale:    1,
			Rotation: rotationDegrees(c.rotation),
		})
	}
	slices.SortFunc(ds, func(a, b contracts.Display) int { return strings.Compare(string(a.ID), string(b.ID)) })
	idx := slices.IndexFunc(ds, func(d contracts.Display) bool { return primaryName != "" && string(d.ID) == primaryName })
	if idx < 0 { // no usable primary: origin output, else first
		idx = slices.IndexFunc(ds, func(d contracts.Display) bool { return d.Bounds.Min == (contracts.Point{}) })
	}
	if idx < 0 && len(ds) > 0 {
		idx = 0
	}
	if idx >= 0 {
		ds[idx].Primary = true
	}
	return ds
}

// rotationDegrees maps the RandR rotation bit to degrees; reflection bits
// are ignored.
func rotationDegrees(r uint16) int {
	switch {
	case r&randr.RotationRotate90 != 0:
		return 90
	case r&randr.RotationRotate180 != 0:
		return 180
	case r&randr.RotationRotate270 != 0:
		return 270
	}
	return 0
}

// parseXftDPI extracts Xft.dpi from an X resource database dump.
func parseXftDPI(db string) float64 {
	sc := bufio.NewScanner(strings.NewReader(db))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(k) != "Xft.dpi" {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f <= 0 {
			return 0
		}
		return f
	}
	return 0
}
