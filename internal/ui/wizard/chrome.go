package wizard

import (
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// The setup frame from the approved prototype: no sidebar; a header with
// the logo and the language, the screen in one centred column, and a
// footer with Back, the speech model's download and the screen's action.

func (w *Wizard) clickable(a Action) *widget.Clickable {
	b, ok := w.btns[a]
	if !ok {
		b = new(widget.Clickable)
		w.btns[a] = b
	}
	return b
}

// clicked reports a click on the action's button since the last frame.
func (w *Wizard) clicked(gtx layout.Context, a Action) bool {
	b, ok := w.btns[a]
	return ok && b.Clicked(gtx)
}

// btn lays out an action button and records it for the frame.
func (w *Wizard) btn(gtx layout.Context, th *theme.Theme, a Action, b kit.ButtonStyle, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, a)
	w.frame.texts = append(w.frame.texts, b.Label)
	if !enabled {
		gtx = gtx.Disabled()
	}
	return b.Layout(gtx, th, w.clickable(a))
}

// Layout draws the setup: header, the screen, footer.
func (w *Wizard) Layout(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	w.frame = frameRecord{}
	s := w.snap()
	if !w.started {
		w.started = true
		// The Gate's own state: the subscribed copy may not have arrived.
		w.enterScreen(screenFor(snapshot{prog: w.cfg.Gate.Progress()}))
	}
	w.update(gtx, s, l)
	s = w.snap()
	w.autoCheck(gtx, s)
	w.backgroundChecks(gtx, s)
	s = w.snap()

	kit.Fill(gtx, th.Pal.Bg)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.header(gtx, th, l) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return w.content(gtx, th, l, s) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.footer(gtx, th, l, s) }),
	)
}

// update handles the clicks of the previous frame.
func (w *Wizard) update(gtx layout.Context, s snapshot, l i18n.Lang) {
	if w.clicked(gtx, ActBack) && w.scr > screenAgent && !s.running {
		w.enterScreen(w.scr - 1)
	}
	if w.clicked(gtx, ActNext) && w.scr < screenDone && s.passed(w.scr) && !s.running {
		w.enterScreen(w.scr + 1)
	}
	if w.clicked(gtx, ActOpen) {
		w.Open()
	}
	if i := w.langDD.Update(gtx, len(i18n.Languages)); i >= 0 && w.cfg.SetLanguage != nil {
		go w.cfg.SetLanguage(i18n.Languages[i].Code)
	}
	switch w.scr {
	case screenAgent:
		w.byok.update(w, gtx, s)
	case screenVoice:
		w.tts.update(w, gtx, s)
		w.stt.update(w, gtx, s, l)
		w.voiceUpdate(gtx, s)
	case screenDone:
		w.doneUpdate(gtx, s)
	}
}

func (w *Wizard) header(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Left: th.Dp(24), Right: th.Dp(16)}.Layout(gtx, kit.FixedH(th, 56, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Spread(
			func(gtx layout.Context) layout.Dimensions { return kit.LogoVersion(gtx, th, 15, "") },
			func(gtx layout.Context) layout.Dimensions {
				if w.cfg.SetLanguage == nil {
					return layout.Dimensions{}
				}
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(190)))
				return w.langPicker(gtx, th, l)
			},
		)(gtx)
	}))
}

// langPicker is the interface language select, each language under its
// own name.
func (w *Wizard) langPicker(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, ActLanguage)
	names := make([]string, len(i18n.Languages))
	sel := 0
	for i, lg := range i18n.Languages {
		names[i] = lg.Native
		if lg.Code == l {
			sel = i
		}
	}
	w.frame.texts = append(w.frame.texts, names[sel])
	if w.langDD.Opened() {
		w.frame.texts = append(w.frame.texts, names...)
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return w.langDD.Layout(gtx, th, names, sel, names[sel])
}

// stepBody is what a screen contributes to the page: rows of the column.
type stepBody struct {
	rows []layout.Widget
}

// contentWidth is the centred column of every screen, in dp.
const contentWidth = 680

func (w *Wizard) content(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	var body stepBody
	var key string
	switch w.scr {
	case screenAgent:
		key, body = "agent", w.byok.body(w, th, l, s)
	case screenVoice:
		key, body = "voice", w.voiceBody(th, l, s)
	case screenDone:
		key, body = "done", w.doneBody(th, l, s)
	}
	head := kit.VStack(th, 8,
		w.text(kit.Heading(th, i18n.T(l, "wiz.screen."+key))),
		w.text(kit.Lead(th, i18n.T(l, "wiz.screen."+key+".intro"))),
	)
	rows := append([]layout.Widget{head}, body.rows...)
	// The list spans the whole window so its scrollbar sits at the edge;
	// each row is the centred column.
	return material.List(th.Theme, &w.list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		side := max((gtx.Constraints.Max.X-gtx.Dp(th.Dp(contentWidth)))/2, gtx.Dp(th.Dp(24)))
		in := layout.Inset{Top: th.Dp(22), Left: gtx.Metric.PxToDp(side), Right: gtx.Metric.PxToDp(side)}
		if i == 0 {
			in.Top = th.Dp(40)
		}
		if i == len(rows)-1 {
			in.Bottom = th.Dp(32)
		}
		return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return rows[i](gtx)
		})
	})
}

// fixText is what to do about a technical code, in plain words.
func fixText(l i18n.Lang, code string) string {
	if k := "wiz.fix." + code; i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "wiz.fix.generic")
}

func (w *Wizard) footer(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	var back layout.Widget
	if w.scr > screenAgent {
		back = func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActBack, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: i18n.T(l, "wiz.back"), Icon: icon.Left}, !s.running)
		}
	}
	chip := func(gtx layout.Context) layout.Dimensions { return w.modelChip(gtx, th, l) }
	var extra layout.Widget
	if w.cfg.AsideExtra != nil {
		extra = func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(280)))
			return w.cfg.AsideExtra(gtx, th, l)
		}
	}
	primary := func(gtx layout.Context) layout.Dimensions {
		if w.scr == screenDone {
			ready := s.prog.State == readiness.Ready
			return w.btn(gtx, th, ActOpen, kit.ButtonStyle{Kind: kit.Primary, Label: i18n.T(l, "wiz.open"), Icon: icon.ArrowRight}, ready)
		}
		return w.btn(gtx, th, ActNext, kit.ButtonStyle{Kind: kit.Primary, Label: i18n.T(l, "wiz.next"), Icon: icon.ArrowRight}, s.passed(w.scr) && !s.running)
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Left: th.Dp(24), Right: th.Dp(24)}.Layout(gtx, kit.FixedH(th, 72, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Spread(kit.HStack(th, 10, back, chip, extra), primary)(gtx)
	}))
}
