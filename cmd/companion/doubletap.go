package main

import (
	"context"

	"github.com/xlsft/companion/internal/doubletap"
	"github.com/xlsft/companion/internal/platform/control"
	"github.com/xlsft/companion/internal/platform/keybind"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/pkg/contracts"
)

// A double tap of one modifier toggles Talk like the Talk chord (OC-133).
// X11 sees it itself; Sway and Hyprland bind each clean tap of the key to
// "ctl tap-<key>" and the control socket counts two. Elsewhere (the
// GlobalShortcuts portal, niri, GNOME) nothing can see a lone modifier.

// doubleTapSupportedLocked reports whether this system can deliver a
// double tap. c.hkMu must be held.
func (c *core) doubleTapSupportedLocked() bool {
	if c.hotkeys == nil {
		return false
	}
	m := c.hotkeyMethod()
	return m.TapBinds() || (m == keybind.MethodService && c.hotkeys.DoubleTapDirect())
}

// doubleTap is the stored double-tap key ("" = off) and whether this
// system supports one at all.
func (c *core) doubleTap() (string, bool) {
	c.hkMu.Lock()
	ok := c.doubleTapSupportedLocked()
	c.hkMu.Unlock()
	return c.settings.Get().Voice.DoubleTapKey, ok
}

// armDoubleTapLocked arms key on the hotkey service. c.hkMu must be held.
func (c *core) armDoubleTapLocked(ctx context.Context, key string) error {
	if c.hotkeys == nil || key == c.hkTap {
		return nil
	}
	if err := c.hotkeys.SetDoubleTap(ctx, control.TalkID, key); err != nil {
		return err
	}
	c.hkTap = key
	return nil
}

// bindTapsLocked rewrites the desktop's block with key as the double-tap
// key, where the desktop binds the taps. c.hkMu must be held.
func (c *core) bindTapsLocked(ctx context.Context, s settings.Settings, key string) error {
	m := c.hotkeyMethod()
	if !m.TapBinds() {
		return nil
	}
	req, err := c.bindRequest(s)
	if err != nil {
		return err
	}
	req.DoubleTap = key
	_, err = keybind.Apply(ctx, m, hotkeyEnv(), req)
	if err != nil && req.Dictate.Key != "" && keybind.ErrorCode(err) == keybind.CodeConflict {
		// The block may hold Talk without a refused Dictate chord.
		req.Dictate = contracts.Hotkey{}
		_, err = keybind.Apply(ctx, m, hotkeyEnv(), req)
	}
	return err
}

// setDoubleTap validates, binds and stores the double-tap key; "" turns
// it off, which always works. A key on a system that cannot see one is
// Unsupported(CapDoubleTap) and nothing changes.
func (c *core) setDoubleTap(ctx context.Context, key string) error {
	if err := doubletap.Check(key); err != nil {
		return err
	}
	c.hkMu.Lock()
	defer c.hkMu.Unlock()
	if key != "" && !c.doubleTapSupportedLocked() {
		return contracts.Unsupported(contracts.CapDoubleTap)
	}
	prev := c.settings.Get()
	old := prev.Voice.DoubleTapKey
	undo := func() {
		if err := c.bindTapsLocked(ctx, prev, old); err != nil {
			c.log.Warn("double tap restore", "code", keybind.ErrorCode(err))
		}
		_ = c.armDoubleTapLocked(ctx, old)
	}
	if err := c.bindTapsLocked(ctx, prev, key); err != nil {
		c.log.Warn("double tap apply", "code", keybind.ErrorCode(err))
		return err
	}
	if err := c.armDoubleTapLocked(ctx, key); err != nil {
		undo()
		return err
	}
	if err := c.settings.Update(ctx, func(s *settings.Settings) { s.Voice.DoubleTapKey = key }); err != nil {
		undo()
		return err
	}
	c.refreshAppliedLocked(ctx)
	return nil
}

// setupPermissions gives the wizard and Settings the double tap.
func (p setupPermissions) DoubleTap() (key string, supported bool) { return p.c.doubleTap() }

func (p setupPermissions) SetDoubleTap(ctx context.Context, key string) error {
	return p.c.setDoubleTap(ctx, key)
}
