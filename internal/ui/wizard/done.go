package wizard

import (
	"context"
	"errors"
	"strings"
	"time"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// The finish: the full test runs by itself, without the user's voice
// (screen, model answer, hint); the voice shortcut; and, only when they
// need the user, the permissions checked in the background.

// doneStages are the full test's stages the finish shows: the voice
// stages were proven on the voice screen.
var doneStages = []string{"capture", "llm", "pointing"}

// endToEndReply is implemented by an STT backend that keeps the model's
// answer from the last full test.
type endToEndReply interface{ LastEndToEndReply() string }

// doubleTapHotkeys is a Permissions backend with the double-tap shortcut:
// key "" is off; SetDoubleTap answers Unsupported where the system cannot
// see a lone key.
type doubleTapHotkeys interface {
	DoubleTap() (key string, supported bool)
	SetDoubleTap(ctx context.Context, key string) error
}

// tapKeys are the double-tap choices after "Off", named by the keys' own
// labels, which need no translation.
var (
	tapKeys   = []string{"ctrl", "shift", "alt", "super"}
	tapLabels = []string{"Ctrl", "Shift", "Alt", "Super"}
)

// tapState is the double-tap row; the fields after dd are guarded by
// Wizard.mu.
type tapState struct {
	dd                                kit.Dropdown
	loaded, supported, saving, failed bool
	key                               string
}

// setDoubleTap saves the key off the UI goroutine.
func (w *Wizard) setDoubleTap(b doubleTapHotkeys, key string) {
	w.mu.Lock()
	w.tap.saving, w.tap.failed = true, false
	w.mu.Unlock()
	go func() {
		err := b.SetDoubleTap(w.ctx, key)
		w.mu.Lock()
		t := &w.tap
		t.saving = false
		switch {
		case errors.Is(err, contracts.ErrUnsupported):
			t.supported = false
		case err != nil:
			t.failed = true
		default:
			t.key = key
		}
		w.mu.Unlock()
		if err != nil {
			w.cfg.Log.Warn("setup double tap not set", "code", runErrCode(err))
		}
		w.cfg.Invalidate()
	}()
}

func (w *Wizard) doneUpdate(gtx layout.Context, s snapshot) {
	if w.clicked(gtx, ActRunFull) && !s.running {
		w.runCheck(readiness.StepEndToEnd, nil, nil)
	}
	if w.clicked(gtx, ActChangeKeys) {
		w.keysShown = !w.keysShown
	}
	if b, ok := w.cfg.Backends.Permissions.(doubleTapHotkeys); ok {
		if !w.tap.loaded {
			key, sup := b.DoubleTap()
			w.mu.Lock()
			w.tap.loaded, w.tap.key, w.tap.supported = true, key, sup
			w.mu.Unlock()
		}
		if i := w.tap.dd.Update(gtx, len(tapKeys)+1); i >= 0 {
			key := ""
			if i > 0 {
				key = tapKeys[i-1]
			}
			w.setDoubleTap(b, key)
		}
	}
	if w.perm.recheck.Swap(false) {
		// Shortcuts were applied: check the permissions again now.
		w.bg.mu.Lock()
		if w.bg.last != nil {
			w.bg.last[readiness.StepPermissions] = time.Time{}
		}
		w.bg.mu.Unlock()
	}
}

func (w *Wizard) doneBody(th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	rows := []layout.Widget{w.donePath(th, l, s)}
	if issues := w.doneIssues(th, l, s); issues != nil {
		rows = append(rows, issues)
	}
	rows = append(rows, w.doneKeys(th, l), w.text(kit.Muted(th, i18n.T(l, "wiz.done.background"))))
	return stepBody{rows: rows}
}

// donePath is the full test: one line per stage, in plain words.
func (w *Wizard) donePath(th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	lines := []layout.Widget{w.text(kit.Medium(kit.Text(th, 14, i18n.T(l, "wiz.done.path"), th.Pal.Fg)))}
	for _, other := range readiness.Steps[:len(readiness.Steps)-1] {
		if r, ok := s.result(other); !ok || r.Status != readiness.Passed {
			if other == readiness.StepPermissions || other == readiness.StepPlatform {
				lines = append(lines, statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.done.waitSystem")))
			} else {
				lines = append(lines, statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.done.finishFirst")))
			}
			return panel(th, kit.VStack(th, 12, lines...))
		}
	}
	failed := false
	for _, stage := range doneStages {
		st, code := stageOf(s, stage)
		name := i18n.T(l, "wiz.done.stage."+stage)
		switch st {
		case stageDone:
			lines = append(lines, statusText(w, th, theme.StatusOK, name))
		case stageFailed:
			failed = true
			lines = append(lines, statusText(w, th, theme.StatusError, name+" — "+fixText(l, code)))
		case stageRunning:
			lines = append(lines, statusText(w, th, theme.StatusUnknown, name+" — "+i18n.T(l, "wiz.state.checking")))
		default:
			lines = append(lines, statusText(w, th, theme.StatusUnknown, name))
		}
	}
	if rp, ok := w.cfg.Backends.STT.(endToEndReply); ok && !s.running {
		if txt := strings.TrimSpace(rp.LastEndToEndReply()); txt != "" {
			lines = append(lines, w.text(kit.Text(th, 13, i18n.Tf(l, "wiz.done.reply", txt), th.Pal.Fg2)))
		}
	}
	if failed {
		lines = append(lines, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActRunFull, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Icon: icon.Refresh2, Label: i18n.T(l, "wiz.e2e.runAgain")}, !s.running)
		})
	}
	return panel(th, kit.VStack(th, 12, lines...))
}

// doneIssues are the permissions that need the user, each with what to
// do; nil when nothing does. A shortcut that needs setting up opens its
// editor right here.
func (w *Wizard) doneIssues(th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	r, ok := s.result(readiness.StepPermissions)
	if !ok || r.Status == readiness.Passed {
		return nil
	}
	var lines []layout.Widget
	needKeys := false
	for _, c := range permissionCaps {
		v := r.Detail[string(c)]
		st, word := permStatus(v)
		if st == theme.StatusOK || v == readiness.PermFallback {
			continue
		}
		if c == contracts.CapGlobalHotkey && v == readiness.PermNeedsBinding {
			needKeys = true
		}
		lines = append(lines, statusText(w, th, theme.StatusError, i18n.T(l, "wiz.perm."+string(c))+" — "+i18n.T(l, word)+". "+fixText(l, v)))
	}
	if len(lines) == 0 {
		return nil
	}
	if needKeys {
		lines = append(lines, w.perm.editor(w).Layout(th, l, &w.frame.texts))
		w.frame.actions = append(w.frame.actions, ActApplyHotkeys)
	}
	return panel(th, kit.VStack(th, 12, lines...))
}

// doneKeys is how to ask by voice: the shortcut, and the double press of
// a lone key where the system lets Companion see one.
func (w *Wizard) doneKeys(th *theme.Theme, l i18n.Lang) layout.Widget {
	talk := ""
	if b := w.cfg.Backends.Permissions; b != nil {
		talk = b.Hotkeys().Talk
	}
	row := func(title, sub string, right layout.Widget) layout.Widget {
		return kit.Spread(kit.VStack(th, 4,
			w.text(kit.Medium(kit.Text(th, 14, title, th.Pal.Fg))),
			w.text(kit.Muted(th, sub))), right)
	}
	keys := func(gtx layout.Context) layout.Dimensions {
		var ch []layout.Widget
		if talk != "" {
			w.frame.texts = append(w.frame.texts, talk)
			ch = append(ch, func(gtx layout.Context) layout.Dimensions { return kit.Kbd(gtx, th, talk) })
		}
		ch = append(ch, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActChangeKeys, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.done.change")}, true)
		})
		return kit.HStack(th, 6, ch...)(gtx)
	}
	w.mu.Lock()
	tap := w.tap
	w.mu.Unlock()
	_, canTap := w.cfg.Backends.Permissions.(doubleTapHotkeys)
	double := func(gtx layout.Context) layout.Dimensions {
		if !canTap || !tap.supported {
			w.frame.texts = append(w.frame.texts, i18n.T(l, "wiz.perm.status.unavailable"))
			return kit.Tag(gtx, th, i18n.T(l, "wiz.perm.status.unavailable"), kit.TagNeutral)
		}
		labels := append([]string{i18n.T(l, "wiz.done.double.off")}, tapLabels...)
		sel := 0
		for i, k := range tapKeys {
			if k == tap.key {
				sel = i + 1
			}
		}
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(140)))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return w.dropdown(gtx, th, &w.tap.dd, labels, sel, labels[0], !tap.saving)
	}
	lines := []layout.Widget{
		w.text(kit.Medium(kit.Text(th, 14, i18n.T(l, "wiz.done.ask"), th.Pal.Fg))),
		row(i18n.T(l, "wiz.done.chord"), i18n.T(l, "wiz.done.chord.d"), keys),
	}
	if w.keysShown {
		lines = append(lines, w.perm.editor(w).Layout(th, l, &w.frame.texts))
		w.frame.actions = append(w.frame.actions, ActApplyHotkeys)
	}
	lines = append(lines, row(i18n.T(l, "wiz.done.double"), i18n.T(l, "wiz.done.double.d"), double))
	if tap.failed {
		lines = append(lines, statusText(w, th, theme.StatusError, i18n.T(l, "wiz.done.double.failed")))
	}
	return panel(th, kit.VStack(th, 14, lines...))
}
