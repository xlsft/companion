package wizard

import (
	"image/color"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// stageState is one end-to-end stage as its card shows it.
type stageState int

const (
	stageWaiting stageState = iota // no run yet
	stageRunning
	stageDone
	stageFailed
	stageNotRun // a run ended before this stage
)

func stageOf(s snapshot, stage string) (stageState, string) {
	if s.running {
		return stageRunning, ""
	}
	r, ok := s.result(readiness.StepEndToEnd)
	if !ok {
		return stageWaiting, ""
	}
	switch v := r.Detail[stage]; v {
	case readiness.DetailOK, "passed":
		return stageDone, ""
	case "", readiness.DetailSkipped, "pending":
		return stageNotRun, ""
	default:
		return stageFailed, v
	}
}

func (w *Wizard) e2eBody(th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	cards := make([]layout.Widget, len(readiness.EndToEndStages))
	for i, stage := range readiness.EndToEndStages {
		cards[i] = w.stageCard(th, l, s, stage)
	}
	return stepBody{rows: []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		// Six across as in the prototype, three when the window is narrow.
		cols := len(cards)
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(780)) {
			cols = 3
		}
		return grid(th, cols, 8, cards...)(gtx)
	}}}
}

func (w *Wizard) stageCard(th *theme.Theme, l i18n.Lang, s snapshot, stage string) layout.Widget {
	st, code := stageOf(s, stage)
	var (
		word      string
		col       color.NRGBA
		ic        icon.Name
		bg        = th.Pal.Surface
		border    = th.Pal.Divider
		nameCol   = th.Pal.Fg
		detail    = i18n.T(l, "wiz.e2e."+stage+".d")
		detailCol = th.Pal.Muted
	)
	switch st {
	case stageDone:
		word, col, ic = "wiz.e2e.done", th.Pal.OK, icon.Check
	case stageFailed:
		word, col, ic = "wiz.e2e.failed", th.Pal.Error, icon.Close
		detail, detailCol = code, th.Pal.Error
	case stageRunning:
		word, col, ic = "wiz.e2e.running", th.Pal.AccentText, icon.Loading3
		bg, border = soft(th, th.Pal.AccentLine, 0.08), th.Pal.AccentLine
	case stageNotRun:
		word, col, ic = "wiz.e2e.notRun", th.Pal.Subtle, icon.Time
		bg, border, nameCol = transparent, th.Pal.Border, th.Pal.Fg2
	default:
		word, col, ic = "wiz.e2e.waiting", th.Pal.Subtle, icon.Time
		bg, border, nameCol = transparent, th.Pal.Border, th.Pal.Fg2
	}
	name := i18n.T(l, "wiz.e2e."+stage)
	wd := i18n.T(l, word)
	line := name + " — " + wd
	if code != "" {
		line += " (" + code + ")"
	}
	w.frame.texts = append(w.frame.texts, line)
	status := hstack(th, 6, iconW(th, ic, 14, col), label(th, 12, wd, col).Layout)
	nm := medium(label(th, 14, name, nameCol))
	dl := label(th, 12, detail, detailCol)
	return func(gtx layout.Context) layout.Dimensions {
		return box(gtx, th, 6, bg, border, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(th.Dp(14)).Layout(gtx, vstack(th, 10, status, nm.Layout, dl.Layout))
		})
	}
}

func (w *Wizard) e2eFooter(th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	b := kit.ButtonStyle{Kind: kit.Primary, Icon: icon.PlayFill, Label: i18n.T(l, "wiz.e2e.run")}
	r, ok := s.result(readiness.StepEndToEnd)
	switch {
	case s.running:
		// Kept in the accent look while running; runCheck ignores clicks
		// until the run ends.
		b.Icon, b.Label = icon.Loading3, i18n.T(l, "wiz.e2e.inProgress")
	case ok:
		b.Icon, b.Label = icon.Refresh2, i18n.T(l, "wiz.e2e.runAgain")
	}
	f := footerSpec{primary: func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, ActRunFull, b, true)
	}}
	if ok && !s.running {
		n := 0
		for _, stage := range readiness.EndToEndStages {
			if st, _ := stageOf(s, stage); st == stageDone {
				n++
			}
		}
		f.status = i18n.Tf(l, "wiz.e2e.stagesPassed", n, len(readiness.EndToEndStages))
		if !r.At.IsZero() {
			f.status += " · " + i18n.Tf(l, "wiz.result.at", r.At.Local().Format("15:04"))
		}
	}
	return f
}
