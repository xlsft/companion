package wizard

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// applyTimeout leaves room for a system dialog confirming the shortcuts.
const applyTimeout = 2 * time.Minute

// HotkeyEditor sets the Talk and Cancel shortcuts: two recorders, Apply
// and the outcome. The permissions step and Settings → Voice show the same
// editor; the platform decides how the chords get bound. Layout runs on
// the UI goroutine, Apply on its own.
type HotkeyEditor struct {
	Backend    Permissions
	Invalidate func()
	// OnApplied runs off the UI goroutine after a successful apply.
	OnApplied func()
	// ApplyBtn, if set, is the Apply button's clickable (the wizard
	// records its actions by button).
	ApplyBtn *widget.Clickable

	talk, cancel kit.ShortcutRecorder
	apply        widget.Clickable
	dirty        bool // the recorders differ from what is stored

	mu       sync.Mutex
	applying bool
	result   *HotkeyStatus // last apply in this editor
	stop     context.CancelFunc
}

// Close cancels an apply in flight.
func (e *HotkeyEditor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stop != nil {
		e.stop()
	}
}

func (e *HotkeyEditor) applyBtn() *widget.Clickable {
	if e.ApplyBtn != nil {
		return e.ApplyBtn
	}
	return &e.apply
}

// Recorders exposes the two fields for tests.
func (e *HotkeyEditor) Recorders() (talk, cancel *kit.ShortcutRecorder) { return &e.talk, &e.cancel }

// status is what the backend reports, or the last apply's failure: a
// rejected chord never reaches the backend's state.
func (e *HotkeyEditor) status() (HotkeyStatus, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.result != nil && e.result.State == HotkeyFailed {
		return *e.result, e.applying
	}
	if e.Backend == nil {
		return HotkeyStatus{}, e.applying
	}
	return e.Backend.Hotkeys(), e.applying
}

// validate checks the draft; the codes are internal/shortcut's.
func (e *HotkeyEditor) validate() (talkCode, cancelCode string) {
	t, err := shortcut.Parse(e.talk.Value())
	if err == nil {
		err = shortcut.Validate(shortcut.Talk, t, t)
	}
	talkCode = shortcut.Code(err)
	c, err := shortcut.Parse(e.cancel.Value())
	if err == nil {
		err = shortcut.Validate(shortcut.Cancel, c, t)
	}
	return talkCode, shortcut.Code(err)
}

func (e *HotkeyEditor) update(gtx layout.Context, st HotkeyStatus, applying bool) {
	if e.talk.Update(gtx) || e.cancel.Update(gtx) {
		e.dirty = true
	}
	if !e.dirty && !e.talk.Recording() && !e.cancel.Recording() {
		e.talk.Set(st.Talk)
		e.cancel.Set(st.Cancel)
	}
	tc, cc := e.validate()
	if e.applyBtn().Clicked(gtx) && !applying && tc == "" && cc == "" && e.Backend != nil {
		e.startApply(e.talk.Value(), e.cancel.Value())
	}
}

func (e *HotkeyEditor) startApply(talk, cancel string) {
	ctx, cancelFn := context.WithTimeout(context.Background(), applyTimeout)
	e.mu.Lock()
	if e.stop != nil {
		e.stop()
	}
	e.applying, e.stop = true, cancelFn
	e.mu.Unlock()
	go func() {
		defer cancelFn()
		st := e.Backend.ApplyHotkeys(ctx, talk, cancel)
		e.mu.Lock()
		e.applying, e.result = false, &st
		e.mu.Unlock()
		if st.State == HotkeyApplied && e.OnApplied != nil {
			e.OnApplied()
		}
		if e.Invalidate != nil {
			e.Invalidate()
		}
	}()
	e.dirty = false
}

// Layout draws the editor; texts receives every string it shows.
func (e *HotkeyEditor) Layout(th *theme.Theme, l i18n.Lang, texts *[]string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		st, applying := e.status()
		e.update(gtx, st, applying)
		return e.view(th, l, st, applying, texts)(gtx)
	}
}

func (e *HotkeyEditor) view(th *theme.Theme, l i18n.Lang, st HotkeyStatus, applying bool, texts *[]string) layout.Widget {
	add := func(s string) string {
		if texts != nil && s != "" {
			*texts = append(*texts, s)
		}
		return s
	}
	tc, cc := e.validate()
	field := func(r *kit.ShortcutRecorder, capKey, code string) layout.Widget {
		cap := add(i18n.T(l, capKey))
		for _, c := range r.Caps() {
			add(c)
		}
		s := kit.RecorderStyle{
			Placeholder: add(i18n.T(l, "hotkey.placeholder")),
			Prompt:      i18n.T(l, "hotkey.prompt"),
			Desc:        cap,
			Invalid:     code != "",
		}
		if r.Recording() {
			add(s.Prompt)
		}
		under := func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }
		if code != "" && !r.Recording() {
			msg := add(i18n.T(l, "hotkey.invalid."+code))
			under = func(gtx layout.Context) layout.Dimensions {
				return kit.Text(th, 12, msg, th.Pal.Error).Layout(gtx)
			}
		}
		return kit.Labeled(th, cap, kit.VStack(th, 4, func(gtx layout.Context) layout.Dimensions {
			return r.Layout(gtx, th, s)
		}, under))
	}
	fields := kit.Grid(th, 2, 12, field(&e.talk, "hotkey.talk", tc), field(&e.cancel, "hotkey.cancel", cc))

	btn := kit.ButtonStyle{Kind: kit.Primary, Small: true, Label: add(i18n.T(l, "hotkey.apply")), Icon: icon.Check}
	if applying {
		btn.Label = add(i18n.T(l, "hotkey.applying"))
	}
	enabled := e.Backend != nil && !applying && tc == "" && cc == ""
	apply := func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		return btn.Layout(gtx, th, e.applyBtn())
	}
	line := kit.HStack(th, 12, apply, e.statusLine(th, l, st, applying, add))
	ws := []layout.Widget{fields, line}
	if st.State == HotkeyApplied && st.ToggleOnly && !e.dirty {
		ws = append(ws, kit.Muted(th, add(i18n.T(l, "hotkey.mode"))).Layout)
	}
	if st.State == HotkeyManual && len(st.Commands) > 0 {
		cmds := strings.Join([]string{
			i18n.T(l, "hotkey.talk") + ":  " + st.Commands[0],
			i18n.T(l, "hotkey.cancel") + ":  " + st.Commands[1],
		}, "\n")
		ws = append(ws, kit.Muted(th, add(i18n.T(l, "hotkey.manual"))).Layout,
			func(gtx layout.Context) layout.Dimensions { return kit.CodeBlock(gtx, th, add(cmds)) })
	}
	return kit.VStack(th, 12, ws...)
}

// statusLine is the outcome beside Apply.
func (e *HotkeyEditor) statusLine(th *theme.Theme, l i18n.Lang, st HotkeyStatus, applying bool, add func(string) string) layout.Widget {
	var s theme.Status
	var txt string
	switch {
	case applying:
		return kit.Muted(th, add(i18n.T(l, "hotkey.applyingHint"))).Layout
	case e.dirty:
		s, txt = theme.StatusUnknown, i18n.T(l, "hotkey.unsaved")
	case st.State == HotkeyApplied:
		label := st.Talk
		if h, err := shortcut.Parse(st.Talk); err == nil {
			label = shortcut.Label(h, runtime.GOOS)
		}
		s, txt = theme.StatusOK, i18n.Tf(l, "hotkey.applied", label)
	case st.State == HotkeyFailed:
		s, txt = theme.StatusError, hotkeyFailure(l, st.Code)
	case st.State == HotkeyManual:
		s, txt = theme.StatusWarn, i18n.T(l, "hotkey.manualShort")
	default:
		s, txt = theme.StatusUnknown, i18n.T(l, "hotkey.notApplied")
	}
	add(txt)
	return func(gtx layout.Context) layout.Dimensions {
		col := th.Pal.Color(s)
		if s == theme.StatusUnknown {
			col = th.Pal.Muted
		}
		return kit.HStack(th, 6, kit.IconW(th, kit.StatusIcon(s), 14, col), kit.Text(th, 13, txt, th.Pal.Fg2).Layout)(gtx)
	}
}

// hotkeyFailure explains an apply failure in the user's words; an unknown
// code is still shown, as a code.
func hotkeyFailure(l i18n.Lang, code string) string {
	k := "hotkey.err." + code
	if i18n.Has(k) {
		return i18n.T(l, k)
	}
	if k = "hotkey.invalid." + code; i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.Tf(l, "hotkey.err.other", code)
}
