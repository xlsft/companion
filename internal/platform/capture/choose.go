package capture

import (
	"context"
	"errors"
	"fmt"

	"github.com/xlsft/companion/pkg/contracts"
)

// Backend names, as reported in CapabilityState.Backend and in
// CaptureMetadata.PlatformIDs["backend"].
const (
	BackendScreencopy       = "wlr-screencopy"
	BackendPortalScreenshot = "portal-screenshot"
	BackendX11              = "x11-getimage"
)

// Candidates lists the backends worth trying for report, best first.
// ScreenCast+PipeWire is not among them yet: on niri/KWin with NVIDIA it
// needs an EGL DMA-BUF import (OC-6 spike), so the Screenshot portal is the
// Wayland fallback for now.
func Candidates(report contracts.CapabilityReport) []string {
	switch report.Session {
	case contracts.SessionWayland:
		var c []string
		// Try screencopy unless the registry probe positively saw no
		// manager: opening it is cheap and fails fast.
		if report.Facts["wayland.screencopy"] != "false" {
			c = append(c, BackendScreencopy)
		}
		if v := report.Facts["portal.Screenshot.version"]; v != "" && v != "absent" {
			c = append(c, BackendPortalScreenshot)
		}
		return c
	case contracts.SessionX11:
		return []string{BackendX11}
	}
	return nil
}

// opener starts one backend; extra resolvers for the active display come
// with it (e.g. the X11 pointer position).
type opener func(ctx context.Context) (grabber, []ActiveDisplay, error)

// choose opens the first candidate that works. When none does the error
// wraps contracts.ErrUnsupported together with each backend's reason.
func choose(ctx context.Context, names []string, openers map[string]opener, topo TopologySource, active []ActiveDisplay) (*Capturer, error) {
	errs := []error{contracts.ErrUnsupported}
	for _, n := range names {
		open, ok := openers[n]
		if !ok {
			continue
		}
		g, extra, err := open(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("%s: %w", n, err))
			continue
		}
		rs := append(append([]ActiveDisplay(nil), active...), extra...)
		return &Capturer{backend: n, topo: topo, g: g, active: rs}, nil
	}
	if len(errs) == 1 {
		return nil, contracts.Unsupported(contracts.CapScreenCapture)
	}
	return nil, &contracts.CapabilityError{Capability: contracts.CapScreenCapture, Err: errors.Join(errs...)}
}
