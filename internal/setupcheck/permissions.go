package setupcheck

import (
	"context"
	"errors"
	"image"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/pkg/contracts"
)

// Permissions is the StepPermissions check: each capability is exercised
// for real once, and Detail holds one permission value per capability.
type Permissions struct {
	Mic    audio.Capturer
	Device func() string // selected microphone; nil or "" is the default
	// MicFrames is how many frames must arrive (default 5, 100 ms).
	MicFrames  int
	MicTimeout time.Duration // default 3 s

	// Screen is nil when no capture backend could start; ScreenErr says why.
	Screen    contracts.ScreenCapturer
	ScreenErr error
	// CaptureTimeout leaves room for a system consent dialog (default 60 s).
	CaptureTimeout time.Duration

	// Hotkey reports the global hotkey's permission value (HotkeyPermission).
	Hotkey func() string

	// Overlay is nil when no click-through host could start; OverlayErr
	// says why. Topology picks the display to open a surface on.
	Overlay        contracts.OverlayHost
	OverlayErr     error
	Topology       func(ctx context.Context) (contracts.Topology, error)
	PanelFallback  bool // hints can fall back to the panel (FR-OVERLAY-009)
	OverlayTimeout time.Duration
}

// Run checks every permission; it passes when all are usable, directly or
// through their documented fallback. Code is the first unusable value.
func (p Permissions) Run(ctx context.Context) readiness.Result {
	vals := []struct {
		capability contracts.Capability
		run        func(context.Context) string
	}{
		{contracts.CapMicrophone, p.microphone},
		{contracts.CapScreenCapture, p.screen},
		{contracts.CapGlobalHotkey, p.hotkey},
		{contracts.CapOverlayClickThrough, p.overlay},
	}
	res := readiness.Result{Status: readiness.Passed, Detail: map[string]string{}}
	for _, v := range vals {
		if ctx.Err() != nil {
			return failed(CodeCanceled)
		}
		got := v.run(ctx)
		res.Detail[string(v.capability)] = got
		if !readiness.PermissionOK(got) && res.Status == readiness.Passed {
			res.Status, res.Code = readiness.Failed, got
		}
	}
	return res
}

// microphone opens the selected device and reads a few frames, which are
// dropped at once (FR-VOICE-012).
func (p Permissions) microphone(ctx context.Context) string {
	if p.Mic == nil {
		return readiness.PermUnavailable
	}
	ctx, cancel := within(ctx, p.MicTimeout, 3*time.Second)
	defer cancel()
	dev := ""
	if p.Device != nil {
		dev = p.Device()
	}
	st, err := p.Mic.Open(ctx, dev)
	if err != nil {
		return micCode(err)
	}
	defer st.Close()
	want := p.MicFrames
	if want <= 0 {
		want = 5
	}
	for n := 0; n < want; {
		select {
		case _, ok := <-st.Frames():
			if !ok {
				if err := st.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
					return micCode(err)
				}
				return CodeMicOpenFailed // the device ended without audio
			}
			n++
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return CodeCanceled
			}
			return CodeMicOpenFailed
		}
	}
	return readiness.PermGranted
}

// screen takes one real capture and discards it unseen.
func (p Permissions) screen(ctx context.Context) string {
	if p.Screen == nil {
		return permCode(p.ScreenErr, readiness.PermUnavailable)
	}
	ctx, cancel := within(ctx, p.CaptureTimeout, time.Minute)
	defer cancel()
	caps, err := p.Screen.Capture(ctx, contracts.CaptureRequest{Target: contracts.CaptureActiveDisplay})
	got := 0
	for _, c := range caps {
		if c != nil && c.Image != nil {
			got++
		}
		if c != nil {
			c.Release()
		}
	}
	switch {
	case err != nil:
		return permCode(err, CodeCaptureFailed)
	case got == 0:
		return CodeCaptureFailed
	}
	return readiness.PermGranted
}

func (p Permissions) hotkey(context.Context) string {
	if p.Hotkey == nil {
		return readiness.PermUnavailable
	}
	return p.Hotkey()
}

// overlay opens a click-through surface, presents a fully transparent
// frame and closes it again; without a host the panel fallback counts.
func (p Permissions) overlay(ctx context.Context) string {
	fallback := func(code string) string {
		if p.PanelFallback {
			return readiness.PermFallback
		}
		return code
	}
	if p.Overlay == nil || p.Topology == nil {
		return fallback(permCode(p.OverlayErr, readiness.PermUnavailable))
	}
	ctx, cancel := within(ctx, p.OverlayTimeout, 5*time.Second)
	defer cancel()
	topo, err := p.Topology(ctx)
	if err != nil || len(topo.Displays) == 0 {
		return fallback(CodeOverlayFailed)
	}
	d := topo.Displays[0]
	for _, x := range topo.Displays {
		if x.Primary {
			d = x
			break
		}
	}
	s, err := p.Overlay.Open(ctx, d.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return CodeCanceled
		}
		return fallback(permCode(err, CodeOverlayFailed))
	}
	w, h := max(1, d.Native.W), max(1, d.Native.H)
	err = s.Present(ctx, image.NewRGBA(image.Rect(0, 0, w, h)))
	cerr := s.Close()
	if err != nil || cerr != nil {
		if errors.Is(err, context.Canceled) {
			return CodeCanceled
		}
		return fallback(CodeOverlayFailed)
	}
	return readiness.PermGranted
}

// HotkeyState is what the hotkey service achieved.
type HotkeyState struct {
	Running bool  // a hotkey service exists
	Primary bool  // a primary backend (key grab, portal) holds the bindings
	Err     error // registration error of the primary backend, if any
	// CtlSeen: a "companion ctl" command reached the control socket since
	// start, so a compositor binding exists and works.
	CtlSeen bool
	// Applied: the shortcuts were bound and verified, e.g. written to the
	// desktop's configuration and reloaded without error.
	Applied bool
}

// HotkeyPermission decides the global hotkey's permission value. Where only
// the control socket exists the shortcuts are bound in the desktop's
// configuration; that counts once it was applied and verified, or once a
// command arrived through such a binding.
func HotkeyPermission(s HotkeyState) string {
	switch {
	case !s.Running:
		return readiness.PermUnavailable
	case errors.Is(s.Err, contracts.ErrPermissionDenied):
		return readiness.PermDenied
	case errors.Is(s.Err, contracts.ErrHotkeyConflict):
		return "hotkey_conflict"
	case s.Primary && s.Err == nil:
		return readiness.PermGranted
	case s.Applied, s.CtlSeen:
		return readiness.PermGranted
	}
	return readiness.PermNeedsBinding
}
