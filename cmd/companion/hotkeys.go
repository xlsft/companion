package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/xlsft/companion/internal/platform/control"
	"github.com/xlsft/companion/internal/platform/hotkeys"
	"github.com/xlsft/companion/internal/platform/keybind"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// hotkeyEnv is where config-based binds are written; tests replace it.
var hotkeyEnv = keybind.DefaultEnv

// startHotkeys registers Talk and Cancel and forwards presses to the
// session. Without any hotkey backend the app still works from the chat.
func (c *core) startHotkeys(ctx context.Context) {
	hk, err := hotkeys.New(ctx, c.report)
	if err != nil {
		c.hotkeysErr = err
		c.log.Warn("hotkeys unavailable", "err", err)
		return
	}
	c.hotkeys = hk
	c.hkMu.Lock()
	err = c.registerLocked(ctx, c.settings.Get())
	c.refreshAppliedLocked(ctx)
	c.hkMu.Unlock()
	if err != nil {
		c.log.Warn("hotkey registration", "err", err)
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for ev := range hk.Events() {
			if hk.Backend() == hotkeys.BackendIPC || c.hotkeyMethod().ConfigBased() {
				c.ctlSeen.Store(true)
			}
			if c.gate != nil && !c.gate.IsReady() {
				continue // setup is not finished: Talk does not exist yet
			}
			if e, ok := session.FromHotkey(ev, control.TalkID, control.CancelID); ok {
				// Through the Talk control path, so Cancel also clears
				// the overlay (OC-65).
				if err := c.ctl.Post(e); err != nil {
					return
				}
			}
		}
	}()
}

// registerLocked registers the chords of s with the hotkey service.
// c.hkMu must be held.
func (c *core) registerLocked(ctx context.Context, s settings.Settings) error {
	talk, cancel, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	if err != nil {
		c.hotkeysErr = err
		return err
	}
	_, err = c.hotkeys.Register(ctx, []contracts.HotkeyBinding{
		{ID: control.TalkID, Hotkey: talk, Description: "Talk to Companion"},
		{ID: control.CancelID, Hotkey: cancel, Description: "Cancel"},
	})
	c.hotkeysErr = err
	c.hkBound = [2]string{s.Voice.TalkHotkey, s.Voice.CancelHotkey}
	return err
}

// rebindHotkeys follows a settings change made outside ApplyHotkeys.
func (c *core) rebindHotkeys(ctx context.Context, s settings.Settings) {
	if c.hotkeys == nil {
		return
	}
	c.hkMu.Lock()
	defer c.hkMu.Unlock()
	if c.hkBound == [2]string{s.Voice.TalkHotkey, s.Voice.CancelHotkey} {
		return
	}
	if err := c.registerLocked(ctx, s); err != nil {
		c.log.Warn("hotkey registration", "err", err)
	}
	c.refreshAppliedLocked(ctx)
}

// hotkeyMethod is how this desktop gets the shortcuts right now; a portal
// that refused to bind has already dropped the service to the socket.
func (c *core) hotkeyMethod() keybind.Method {
	if c.hotkeys == nil {
		return keybind.MethodManual
	}
	return keybind.Resolve(c.report.Compositor, c.report.Desktop, c.hotkeys.Backend() != hotkeys.BackendIPC)
}

// refreshAppliedLocked derives the status from what is registered and
// what the desktop's configuration holds, e.g. binds written by an
// earlier run. c.hkMu must be held.
func (c *core) refreshAppliedLocked(ctx context.Context) {
	m := c.hotkeyMethod()
	st := wizard.HotkeyStatus{ToggleOnly: m.ToggleOnly() && m != keybind.MethodManual}
	switch {
	case m == keybind.MethodService && c.hotkeysErr == nil:
		st.State = wizard.HotkeyApplied
	case m == keybind.MethodService:
		st.State, st.Code = wizard.HotkeyFailed, hotkeyErrCode(c.hotkeysErr)
	case m.ConfigBased():
		if req, err := c.bindRequest(c.settings.Get()); err == nil && keybind.Applied(ctx, m, hotkeyEnv(), req) {
			st.State = wizard.HotkeyApplied
		}
	}
	c.hkStatus = st
}

func (c *core) bindRequest(s settings.Settings) (keybind.Request, error) {
	talk, cancel, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	return keybind.Request{Talk: talk, Cancel: cancel, Exe: selfPath()}, err
}

// selfPath is this binary, so binds work without companion on $PATH.
func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "companion"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return exe
}

// hotkeyStatus is the editor's view of the shortcuts.
func (c *core) hotkeyStatus() wizard.HotkeyStatus {
	s := c.settings.Get()
	c.hkMu.Lock()
	st := c.hkStatus
	c.hkMu.Unlock()
	st.Talk, st.Cancel = s.Voice.TalkHotkey, s.Voice.CancelHotkey
	if c.hotkeys == nil {
		st.State, st.Code = wizard.HotkeyFailed, keybind.CodeUnsupported
	}
	if c.hotkeys != nil && c.hotkeyMethod() == keybind.MethodManual {
		st.State = wizard.HotkeyManual
		t, cn := keybind.Commands(selfPath())
		st.Commands = []string{t, cn}
	}
	return st
}

// applyHotkeys stores the chords and binds them the way this desktop
// allows: the hotkey service (key grab, portal, OS API), or the desktop's
// configuration through keybind.
func (c *core) applyHotkeys(ctx context.Context, talk, cancel string) wizard.HotkeyStatus {
	failed := func(code string) wizard.HotkeyStatus {
		st := c.hotkeyStatus()
		st.State, st.Code = wizard.HotkeyFailed, code
		st.Talk, st.Cancel = talk, cancel
		return st
	}
	t, cn, err := shortcut.ValidatePair(talk, cancel)
	if err != nil {
		return failed(shortcut.Code(err))
	}
	talk, cancel = shortcut.Format(t), shortcut.Format(cn)
	if c.hotkeys == nil {
		return failed(keybind.CodeUnsupported)
	}
	c.hkMu.Lock()
	defer c.hkMu.Unlock()
	prev := c.settings.Get()
	next := prev
	next.Voice.TalkHotkey, next.Voice.CancelHotkey = talk, cancel
	regErr := c.registerLocked(ctx, next)
	m := c.hotkeyMethod()
	st := wizard.HotkeyStatus{Talk: talk, Cancel: cancel, ToggleOnly: m.ToggleOnly() && m != keybind.MethodManual}
	switch {
	case m == keybind.MethodManual:
		st.State = wizard.HotkeyManual
		a, b := keybind.Commands(selfPath())
		st.Commands = []string{a, b}
	case m == keybind.MethodService && regErr != nil:
		st.State, st.Code = wizard.HotkeyFailed, hotkeyErrCode(regErr)
	case m == keybind.MethodService:
		st.State = wizard.HotkeyApplied
	default:
		_, err := keybind.Apply(ctx, m, hotkeyEnv(), keybind.Request{Talk: t, Cancel: cn, Exe: selfPath()})
		if err != nil {
			st.State, st.Code = wizard.HotkeyFailed, keybind.ErrorCode(err)
		} else {
			st.State = wizard.HotkeyApplied
		}
	}
	if st.State != wizard.HotkeyFailed {
		if err := c.settings.Update(ctx, func(s *settings.Settings) {
			s.Voice.TalkHotkey, s.Voice.CancelHotkey = talk, cancel
		}); err != nil {
			st.State, st.Code = wizard.HotkeyFailed, "save_failed"
		}
	}
	if st.State == wizard.HotkeyFailed {
		// What worked before keeps working: the desktop's config is
		// untouched on failure, so the old chords go back to the service.
		c.log.Warn("hotkey apply", "method", string(m), "code", st.Code)
		if err := c.registerLocked(ctx, prev); err != nil {
			c.log.Warn("hotkey registration", "err", err)
		}
		c.refreshAppliedLocked(ctx)
		return st
	}
	c.log.Info("hotkeys applied", "method", string(m))
	c.hkStatus = st
	return st
}

func hotkeyErrCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, contracts.ErrPermissionDenied):
		return "denied"
	case errors.Is(err, contracts.ErrHotkeyConflict):
		return keybind.CodeConflict
	case errors.Is(err, contracts.ErrUnsupported):
		return keybind.CodeUnsupported
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	}
	if code := shortcut.Code(err); code != "" {
		return code
	}
	return "register_failed"
}

// talkLabel is the Talk chord for running text, e.g. "Super+Space".
func (c *core) talkLabel() string {
	h, err := shortcut.Parse(c.settings.Get().Voice.TalkHotkey)
	if err != nil {
		return ""
	}
	return shortcut.Label(h, runtime.GOOS)
}
