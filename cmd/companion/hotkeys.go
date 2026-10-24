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

// startHotkeys registers Talk, Cancel and Dictate and forwards presses to
// the session and to dictation. Without any hotkey backend the app still
// works from the chat.
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
			if c.routeDictation(ev) {
				continue
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

// routeDictation hands the dictation shortcut to dictation, and Cancel
// too while a dictation runs; it reports whether ev was taken.
func (c *core) routeDictation(ev contracts.HotkeyEvent) bool {
	if c.dictation == nil {
		return false
	}
	switch ev.ID {
	case control.DictateID:
		c.dictation.Hotkey(ev.Kind)
		return true
	case control.CancelID:
		return ev.Kind != contracts.HotkeyReleased && c.dictation.Cancel()
	}
	return false
}

// chordsOf are the stored chords in registration order.
func chordsOf(s settings.Settings) [3]string {
	return [3]string{s.Voice.TalkHotkey, s.Voice.CancelHotkey, s.Voice.DictateHotkey}
}

// dictateOf is the stored Dictate chord when it is set and valid beside
// talk and cancel.
func dictateOf(s settings.Settings, talk, cancel contracts.Hotkey) (contracts.Hotkey, bool) {
	if s.Voice.DictateHotkey == "" {
		return contracts.Hotkey{}, false
	}
	d, err := shortcut.ValidateDictate(s.Voice.DictateHotkey, talk, cancel)
	return d, err == nil
}

// registerLocked registers the chords of s with the hotkey service. A
// Dictate chord the system refuses must not cost Talk, so on failure Talk
// and Cancel are registered alone and the refusal kept in dictateErr.
// c.hkMu must be held.
func (c *core) registerLocked(ctx context.Context, s settings.Settings) error {
	c.hkBound = chordsOf(s)
	c.dictateErr = nil
	talk, cancel, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	if err != nil {
		c.hotkeysErr = err
		return err
	}
	base := []contracts.HotkeyBinding{
		{ID: control.TalkID, Hotkey: talk, Description: "Talk to Companion"},
		{ID: control.CancelID, Hotkey: cancel, Description: "Cancel"},
	}
	if d, ok := dictateOf(s, talk, cancel); ok {
		_, err = c.hotkeys.Register(ctx, append(base, contracts.HotkeyBinding{ID: control.DictateID, Hotkey: d, Description: "Dictate into the focused field"}))
		if err == nil || ctx.Err() != nil {
			c.hotkeysErr = err
			return err
		}
		c.dictateErr = err
	}
	_, err = c.hotkeys.Register(ctx, base)
	c.hotkeysErr = err
	return err
}

// rebindHotkeys follows a settings change made outside the editors.
func (c *core) rebindHotkeys(ctx context.Context, s settings.Settings) {
	if c.hotkeys == nil {
		return
	}
	c.hkMu.Lock()
	defer c.hkMu.Unlock()
	if c.hkBound == chordsOf(s) {
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
// earlier run that predates the Dictate chord. c.hkMu must be held.
func (c *core) refreshAppliedLocked(ctx context.Context) {
	m := c.hotkeyMethod()
	s := c.settings.Get()
	st := wizard.HotkeyStatus{ToggleOnly: m.ToggleOnly() && m != keybind.MethodManual}
	switch {
	case m == keybind.MethodService && c.hotkeysErr == nil:
		st.State = wizard.HotkeyApplied
		if s.Voice.DictateHotkey != "" {
			st.DictateState = wizard.HotkeyApplied
			if c.dictateErr != nil {
				st.DictateState, st.DictateCode = wizard.HotkeyFailed, hotkeyErrCode(c.dictateErr)
			}
		}
	case m == keybind.MethodService:
		st.State, st.Code = wizard.HotkeyFailed, hotkeyErrCode(c.hotkeysErr)
		st.DictateState, st.DictateCode = st.State, st.Code
	case m.ConfigBased():
		req, err := c.bindRequest(s)
		if err != nil {
			break
		}
		env := hotkeyEnv()
		if keybind.Applied(ctx, m, env, req) {
			st.State = wizard.HotkeyApplied
			if req.Dictate.Key != "" {
				st.DictateState = wizard.HotkeyApplied
			}
			break
		}
		req.Dictate = contracts.Hotkey{}
		if keybind.Applied(ctx, m, env, req) {
			st.State = wizard.HotkeyApplied
		}
	}
	c.hkStatus = st
}

func (c *core) bindRequest(s settings.Settings) (keybind.Request, error) {
	talk, cancel, err := shortcut.ValidatePair(s.Voice.TalkHotkey, s.Voice.CancelHotkey)
	req := keybind.Request{Talk: talk, Cancel: cancel, Exe: selfPath()}
	if d, ok := dictateOf(s, talk, cancel); ok {
		req.Dictate = d
	}
	return req, err
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

// manualCommands are the ctl commands to bind by hand: Talk, Cancel and,
// when set, Dictate.
func manualCommands(dictate string) []string {
	t, cn := keybind.Commands(selfPath())
	cmds := []string{t, cn}
	if dictate != "" {
		cmds = append(cmds, keybind.DictateCommand(selfPath()))
	}
	return cmds
}

// hotkeyStatus is the editors' view of the shortcuts.
func (c *core) hotkeyStatus() wizard.HotkeyStatus {
	s := c.settings.Get()
	c.hkMu.Lock()
	st := c.hkStatus
	c.hkMu.Unlock()
	st.Talk, st.Cancel, st.Dictate = s.Voice.TalkHotkey, s.Voice.CancelHotkey, s.Voice.DictateHotkey
	if c.hotkeys == nil {
		st.State, st.Code = wizard.HotkeyFailed, keybind.CodeUnsupported
		st.DictateState, st.DictateCode = st.State, st.Code
	}
	if c.hotkeys != nil && c.hotkeyMethod() == keybind.MethodManual {
		st.State, st.DictateState = wizard.HotkeyManual, wizard.HotkeyManual
		st.Commands = manualCommands(st.Dictate)
	}
	return st
}

// applyHotkeys stores the Talk and Cancel chords and binds them, keeping
// the stored Dictate chord.
func (c *core) applyHotkeys(ctx context.Context, talk, cancel string) wizard.HotkeyStatus {
	return c.applyChords(ctx, talk, cancel, c.settings.Get().Voice.DictateHotkey, false)
}

// applyDictateHotkey stores and binds the Dictate chord beside the stored
// Talk and Cancel.
func (c *core) applyDictateHotkey(ctx context.Context, dictate string) wizard.HotkeyStatus {
	v := c.settings.Get().Voice
	return c.applyChords(ctx, v.TalkHotkey, v.CancelHotkey, dictate, true)
}

// applyChords binds the three chords the way this desktop allows: the
// hotkey service (key grab, portal, OS API), or the desktop's
// configuration through keybind. forDictate says which editor asked: a
// Dictate chord that cannot be bound fails that editor's apply, and only
// costs the Dictate shortcut when Talk's editor applies.
func (c *core) applyChords(ctx context.Context, talk, cancel, dictate string, forDictate bool) wizard.HotkeyStatus {
	failed := func(code string) wizard.HotkeyStatus {
		st := c.hotkeyStatus()
		if forDictate {
			st.DictateState, st.DictateCode = wizard.HotkeyFailed, code
		} else {
			st.State, st.Code = wizard.HotkeyFailed, code
		}
		st.Talk, st.Cancel, st.Dictate = talk, cancel, dictate
		return st
	}
	t, cn, err := shortcut.ValidatePair(talk, cancel)
	if err != nil {
		return failed(shortcut.Code(err))
	}
	var d contracts.Hotkey
	if dictate != "" {
		if d, err = shortcut.ValidateDictate(dictate, t, cn); err != nil {
			return failed(shortcut.Code(err))
		}
		dictate = shortcut.Format(d)
	}
	talk, cancel = shortcut.Format(t), shortcut.Format(cn)
	if c.hotkeys == nil {
		return failed(keybind.CodeUnsupported)
	}
	c.hkMu.Lock()
	defer c.hkMu.Unlock()
	prev := c.settings.Get()
	next := prev
	next.Voice.TalkHotkey, next.Voice.CancelHotkey, next.Voice.DictateHotkey = talk, cancel, dictate
	regErr := c.registerLocked(ctx, next)
	m := c.hotkeyMethod()
	st := wizard.HotkeyStatus{Talk: talk, Cancel: cancel, Dictate: dictate, ToggleOnly: m.ToggleOnly() && m != keybind.MethodManual}
	ok := func() {
		st.State = wizard.HotkeyApplied
		if dictate != "" {
			st.DictateState = wizard.HotkeyApplied
		}
	}
	switch {
	case m == keybind.MethodManual:
		st.State, st.DictateState = wizard.HotkeyManual, wizard.HotkeyManual
		st.Commands = manualCommands(dictate)
	case m == keybind.MethodService && regErr != nil:
		st.State, st.Code = wizard.HotkeyFailed, hotkeyErrCode(regErr)
		st.DictateState, st.DictateCode = st.State, st.Code
	case m == keybind.MethodService && c.dictateErr != nil:
		st.State = wizard.HotkeyApplied
		st.DictateState, st.DictateCode = wizard.HotkeyFailed, hotkeyErrCode(c.dictateErr)
	case m == keybind.MethodService:
		ok()
	default:
		req := keybind.Request{Talk: t, Cancel: cn, Dictate: d, Exe: selfPath()}
		_, err := keybind.Apply(ctx, m, hotkeyEnv(), req)
		if err != nil && !forDictate && d.Key != "" && keybind.ErrorCode(err) == keybind.CodeConflict {
			// The Dictate chord may be the one taken: Talk still goes in.
			dErr := err
			req.Dictate = contracts.Hotkey{}
			if _, err = keybind.Apply(ctx, m, hotkeyEnv(), req); err == nil {
				st.State = wizard.HotkeyApplied
				st.DictateState, st.DictateCode = wizard.HotkeyFailed, keybind.ErrorCode(dErr)
				break
			}
		}
		if err != nil {
			st.State, st.Code = wizard.HotkeyFailed, keybind.ErrorCode(err)
			st.DictateState, st.DictateCode = st.State, st.Code
		} else {
			ok()
		}
	}
	// The asking editor's own outcome decides whether anything is kept.
	outcome, code := st.State, st.Code
	if forDictate {
		outcome, code = st.DictateState, st.DictateCode
	}
	if outcome != wizard.HotkeyFailed {
		if err := c.settings.Update(ctx, func(s *settings.Settings) {
			s.Voice.TalkHotkey, s.Voice.CancelHotkey, s.Voice.DictateHotkey = talk, cancel, dictate
		}); err != nil {
			outcome, code = wizard.HotkeyFailed, "save_failed"
		}
	}
	if outcome == wizard.HotkeyFailed {
		// What worked before keeps working: the desktop's config is
		// untouched on failure, so the old chords go back to the service.
		c.log.Warn("hotkey apply", "method", string(m), "code", code, "dictate", forDictate)
		if err := c.registerLocked(ctx, prev); err != nil {
			c.log.Warn("hotkey registration", "err", err)
		}
		c.refreshAppliedLocked(ctx)
		fst := c.hkStatus
		fst.Talk, fst.Cancel, fst.Dictate = talk, cancel, dictate
		if forDictate {
			fst.DictateState, fst.DictateCode = wizard.HotkeyFailed, code
		} else {
			fst.State, fst.Code = wizard.HotkeyFailed, code
		}
		return fst
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
func (c *core) talkLabel() string { return chordLabel(c.settings.Get().Voice.TalkHotkey) }

// chordLabel is a stored chord for running text, "" for none.
func chordLabel(chord string) string {
	h, err := shortcut.Parse(chord)
	if err != nil {
		return ""
	}
	return shortcut.Label(h, runtime.GOOS)
}
