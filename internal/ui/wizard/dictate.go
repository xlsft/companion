package wizard

import (
	"context"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/shortcut"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// DictateEditor sets the dictation shortcut (OC-78): one recorder, Apply
// and the outcome, the same parts as the Talk editor. Under the hood the
// chord is bound with Talk and Cancel, the way this desktop binds keys.
type DictateEditor struct {
	Backend    DictateHotkeys
	Invalidate func()

	rec   kit.ShortcutRecorder
	apply widget.Clickable
	dirty bool

	mu       sync.Mutex
	applying bool
	result   *HotkeyStatus
	stop     context.CancelFunc
}

// Recorder exposes the field for tests.
func (e *DictateEditor) Recorder() *kit.ShortcutRecorder { return &e.rec }

// Close cancels an apply in flight.
func (e *DictateEditor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stop != nil {
		e.stop()
	}
}

func (e *DictateEditor) status() (HotkeyStatus, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.result != nil && e.result.DictateState == HotkeyFailed {
		return *e.result, e.applying
	}
	if e.Backend == nil {
		return HotkeyStatus{}, e.applying
	}
	return e.Backend.Hotkeys(), e.applying
}

// validate checks the draft against the stored Talk and Cancel.
func (e *DictateEditor) validate(st HotkeyStatus) string {
	t, _ := shortcut.Parse(st.Talk)
	c, _ := shortcut.Parse(st.Cancel)
	_, err := shortcut.ValidateDictate(e.rec.Value(), t, c)
	return shortcut.Code(err)
}

func (e *DictateEditor) update(gtx layout.Context, st HotkeyStatus, applying bool) string {
	if e.rec.Update(gtx) {
		e.dirty = true
	}
	if !e.dirty && !e.rec.Recording() {
		e.rec.Set(st.Dictate)
	}
	code := e.validate(st)
	if e.apply.Clicked(gtx) && !applying && code == "" && e.Backend != nil {
		e.startApply(e.rec.Value())
	}
	return code
}

func (e *DictateEditor) startApply(chord string) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	e.mu.Lock()
	if e.stop != nil {
		e.stop()
	}
	e.applying, e.stop = true, cancel
	e.mu.Unlock()
	go func() {
		defer cancel()
		st := e.Backend.ApplyDictateHotkey(ctx, chord)
		e.mu.Lock()
		e.applying, e.result = false, &st
		e.mu.Unlock()
		if e.Invalidate != nil {
			e.Invalidate()
		}
	}()
	e.dirty = false
}

// Layout draws the editor; texts receives every string it shows.
func (e *DictateEditor) Layout(th *theme.Theme, l i18n.Lang, texts *[]string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		st, applying := e.status()
		code := e.update(gtx, st, applying)
		return e.view(th, l, st, applying, code, texts)(gtx)
	}
}

func (e *DictateEditor) view(th *theme.Theme, l i18n.Lang, st HotkeyStatus, applying bool, code string, texts *[]string) layout.Widget {
	add := func(s string) string {
		if texts != nil && s != "" {
			*texts = append(*texts, s)
		}
		return s
	}
	// Half the width, like one of the Talk editor's two fields.
	field := kit.Grid(th, 2, 12, shortcutField(th, l, &e.rec, "dictation.shortcut", code, add), layout.Spacer{}.Layout)
	btn := kit.ButtonStyle{Kind: kit.Primary, Small: true, Label: add(i18n.T(l, "hotkey.apply")), Icon: icon.Check}
	if applying {
		btn.Label = add(i18n.T(l, "hotkey.applying"))
	}
	enabled := e.Backend != nil && !applying && code == ""
	apply := func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		return btn.Layout(gtx, th, &e.apply)
	}
	line := kit.HStack(th, 12, apply, statusLine(th, l, applyView{
		state: st.DictateState, code: st.DictateCode, chord: st.Dictate, appliedKey: "dictation.applied",
		applying: applying, dirty: e.dirty,
	}, add))
	ws := []layout.Widget{field, line}
	if st.DictateState == HotkeyApplied && st.ToggleOnly && !e.dirty {
		ws = append(ws, kit.Muted(th, add(i18n.T(l, "dictation.mode"))).Layout)
	}
	if st.DictateState == HotkeyManual && len(st.Commands) > 2 {
		cmd := i18n.T(l, "dictation.shortcut") + ":  " + st.Commands[2]
		ws = append(ws, kit.Muted(th, add(i18n.T(l, "hotkey.manual"))).Layout,
			func(gtx layout.Context) layout.Dimensions { return kit.CodeBlock(gtx, th, add(cmd)) })
	}
	return kit.VStack(th, 12, ws...)
}
