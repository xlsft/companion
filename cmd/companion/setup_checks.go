package main

import (
	"context"
	"errors"

	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/platform/hotkeys"
	"github.com/xlsft/companion/internal/platform/keybind"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/setupcheck"
	"github.com/xlsft/companion/internal/stt/supervisor"
	"github.com/xlsft/companion/internal/talk"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// setupChecks are the wizard's step checks over the core's real services
// (FR-ONB-005…009); the decisions live in internal/setupcheck.
func (c *core) setupChecks() map[readiness.Step]readiness.Check {
	lang := func() string { return c.settings.Get().UI.Language }
	listener := c.setupListener()
	c.sttCheck = &setupcheck.STT{Listener: listener, Lang: lang}

	perms := setupcheck.Permissions{
		Mic:           c.mic,
		Device:        func() string { return micDevice(c.settings.Get()) },
		ScreenErr:     c.captureErr,
		Hotkey:        func() string { return setupcheck.HotkeyPermission(c.hotkeyState()) },
		OverlayErr:    c.overlayErr,
		PanelFallback: c.report.State(contracts.CapOverlayFallbackPanel).Status == contracts.StatusAvailable,
	}
	e2e := setupcheck.EndToEnd{
		Listener:   listener,
		Lang:       lang,
		Target:     captureRequest(c.settings.Get()),
		Provider:   c.talkProvider,
		Speaker:    c.speaker,
		Configured: c.speaker.configured,
	}
	// Typed nils must not reach the interfaces: the checks test for nil.
	if c.capturer != nil {
		perms.Screen, e2e.Capturer = c.capturer, c.capturer
	}
	if c.overlayHost != nil && c.displays != nil {
		perms.Overlay, perms.Topology = c.overlayHost, c.displays.Topology
	}
	if c.overlay != nil {
		e2e.Hints = c.overlay
	}
	return map[readiness.Step]readiness.Check{
		readiness.StepPermissions: perms.Run,
		readiness.StepBYOK:        setupcheck.BYOK{Providers: c.providers}.Run,
		readiness.StepSTT:         c.sttCheck.Run,
		readiness.StepTTS: setupcheck.Speak{
			Speaker: c.speaker, Configured: c.speaker.configured, Lang: lang,
		}.Run,
		readiness.StepPlatform: setupcheck.Platform{Detect: detect.New().Detect}.Run,
		readiness.StepEndToEnd: func(ctx context.Context) readiness.Result {
			// The capture target may have changed since startup.
			e := e2e
			e.Target = captureRequest(c.settings.Get())
			return e.Run(ctx)
		},
	}
}

// setupListener records through the supervised sidecar like Talk does.
func (c *core) setupListener() *setupcheck.Listener {
	return &setupcheck.Listener{
		Mic:    c.mic,
		Device: func() string { return micDevice(c.settings.Get()) },
		STT:    talk.SupervisedSTT(c.stt),
		Model: func() error {
			_, err := c.activator.Active()
			return err
		},
		Ready: c.sttReady,
	}
}

// sttReady brings the sidecar up if it is not running (no model at
// startup, or crash-looped) and waits for it.
func (c *core) sttReady(ctx context.Context) error {
	switch c.stt.State().Kind {
	case supervisor.Stopped, supervisor.Degraded:
	default:
		return c.stt.WaitReady(ctx)
	}
	if c.runCtx == nil {
		return errors.New("core not started")
	}
	c.restartSTT()
	// Reset is asynchronous: a Degraded seen before the supervisor left it
	// is the old state, not the outcome of this restart.
	ch, unsub := c.stt.Subscribe()
	defer unsub()
	left := false
	for {
		select {
		case st, ok := <-ch:
			if !ok {
				return supervisor.ErrStopped
			}
			switch st.Kind {
			case supervisor.Ready:
				return nil
			case supervisor.Degraded:
				if left {
					return supervisor.ErrDegraded
				}
			default:
				left = true
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *core) hotkeyState() setupcheck.HotkeyState {
	if c.hotkeys == nil {
		return setupcheck.HotkeyState{}
	}
	c.hkMu.Lock()
	applied := c.hkStatus.State == wizard.HotkeyApplied
	err := c.hotkeysErr
	c.hkMu.Unlock()
	m := c.hotkeyMethod()
	return setupcheck.HotkeyState{
		Running: true,
		// Hyprland's portal holds the shortcuts without binding keys, so
		// only a service that binds chords itself counts as primary.
		Primary: m == keybind.MethodService && c.hotkeys.Backend() != hotkeys.BackendIPC,
		Err:     err,
		CtlSeen: c.ctlSeen.Load(),
		Applied: applied,
	}
}
