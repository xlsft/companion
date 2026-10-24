package wizard

import (
	"gioui.org/layout"
	"gioui.org/text"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

func (b *byokState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	w.mu.Lock()
	info, saving, saveErr, loaded := b.info, b.saving, b.saveErr, b.loaded
	w.mu.Unlock()
	enabled := w.cfg.Backends.Provider != nil && !saving && !s.running
	return stepBody{rows: []layout.Widget{
		columns(th, 360, 40, 840, b.form(w, th, l, info, enabled, loaded, saveErr), b.side(w, th, l, s, info)),
	}}
}

func (b *byokState) form(w *Wizard, th *theme.Theme, l i18n.Lang, info *providers.Info, enabled, loaded bool, saveErr string) layout.Widget {
	kind := func(k llm.Kind) layout.Widget {
		id := "openai"
		if k == llm.KindAnthropic {
			id = "anthropic"
		}
		title, sub := i18n.T(l, "wiz.byok.kind."+id), i18n.T(l, "wiz.byok.kind."+id+".d")
		w.frame.texts = append(w.frame.texts, title, sub)
		return func(gtx layout.Context) layout.Dimensions {
			if !enabled {
				gtx = gtx.Disabled()
			}
			return radioCard(gtx, th, &b.kind, string(k), title, sub)
		}
	}
	keyField := field(w, th, i18n.T(l, "wiz.byok.key"), i18n.T(l, "wiz.byok.key.hint"), &b.key, enabled)
	if info != nil && info.HasKey && !b.replacing {
		keyField = storedKey(w, th, l, i18n.T(l, "wiz.byok.key"), enabled)
	}
	hdrB := kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.byok.headers"), Icon: icon.Add}
	var headers layout.Widget
	if b.showHeaders {
		hdrB.Icon = icon.Up
		headers = field(w, th, i18n.T(l, "wiz.byok.headers.hint"), "X-Header: value", &b.hdr, enabled)
	}
	var status layout.Widget
	switch {
	case w.cfg.Backends.Provider == nil:
		status = badgeLine(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"))
	case saveErr != "":
		status = vstack(th, 4, badgeLine(w, th, theme.StatusError, i18n.Tf(l, "wiz.byok.saveFailed", saveErr)),
			w.text(kit.Muted(th, fixText(l, saveErr))))
	case !loaded:
		status = w.text(kit.Muted(th, i18n.T(l, "wiz.loading")))
	}
	return vstack(th, 22,
		flow(th, 220, 8, kind(llm.KindAnthropic), kind(llm.KindOpenAI)),
		vstack(th, 14,
			field(w, th, i18n.T(l, "wiz.byok.baseURL"), "https://api.example.com/v1", &b.baseURL, enabled),
			flow(th, 200, 12,
				field(w, th, i18n.T(l, "wiz.byok.model"), i18n.T(l, "wiz.byok.model.hint"), &b.model, enabled),
				keyField),
			headers,
		),
		spread(func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActHeaders, hdrB, enabled)
		}, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActSave, kit.ButtonStyle{Kind: kit.Secondary, Label: i18n.T(l, "wiz.save")}, enabled)
		}),
		status,
	)
}

// badgeLine is a status icon and text on one line, recorded for the frame.
func badgeLine(w *Wizard, th *theme.Theme, s theme.Status, txt string) layout.Widget {
	w.frame.texts = append(w.frame.texts, txt)
	return statusText(th, s, txt, 13, th.Pal.Color(s))
}

// providerSteps are the provider test steps shown as rows; the
// capability step is the rows under the divider.
var providerSteps = []string{providers.StepConnection, providers.StepAuthModel}

// providerCaps are the probed capabilities shown under the divider.
var providerCaps = []string{"text", "stream", "vision", "tools"}

// stepMark maps a provider test status to its status.
func stepMark(v string) theme.Status {
	switch providers.Status(v) {
	case providers.StatusOK:
		return theme.StatusOK
	case providers.StatusFailed:
		return theme.StatusError
	}
	return theme.StatusUnknown
}

// checkLine is a row of a check list: status icon, name, detail on the
// right. Its accessible text is "name — detail".
func checkLine(w *Wizard, th *theme.Theme, h float32, ds theme.Status, name, right string) layout.Widget {
	w.frame.texts = append(w.frame.texts, name+" — "+right)
	col := th.Pal.Color(ds)
	if ds == theme.StatusUnknown {
		col = th.Pal.Subtle
	}
	return fixedH(th, h, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(iconW(th, kit.StatusIcon(ds), 16, col)),
			kit.HSpace(th.Dp(10)),
			layout.Flexed(1, label(th, 14, name, th.Pal.Fg).Layout),
			kit.HSpace(th.Dp(10)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(200)))
				l := label(th, 12, right, th.Pal.Muted)
				l.Alignment = text.End
				return l.Layout(gtx)
			}),
		)
	})
}

// side is the connection check panel and the privacy note.
func (b *byokState) side(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot, info *providers.Info) layout.Widget {
	r, ok := s.result(readiness.StepBYOK)
	detail := map[string]string{}
	if ok && r.Detail != nil {
		detail = r.Detail
	}
	head := w.text(medium(label(th, 14, i18n.T(l, "wiz.byok.panel"), th.Pal.Fg)))
	word := i18n.T(l, "wiz.state.todo")
	switch {
	case s.running:
		word = i18n.T(l, "wiz.state.checking")
	case ok && r.Status == readiness.Passed:
		word = i18n.T(l, "wiz.state.passed")
	case ok:
		word = i18n.T(l, "wiz.state.failed")
	}
	state := w.text(kit.Muted(th, word))

	var steps, caps []layout.Widget
	for _, id := range providerSteps {
		right := i18n.T(l, "wiz.state.todo")
		if ok {
			right = i18n.T(l, "wiz.state.skipped")
		}
		if k := detail[id+providers.DetailMessage]; k != "" {
			right = i18n.T(l, k)
		}
		if c := detail[id+providers.DetailCode]; c != "" {
			right += " (" + c + ")"
		}
		steps = append(steps, checkLine(w, th, 38, stepMark(detail[id]), i18n.T(l, "wiz.byok.step."+id), right))
	}
	// A failed capability probe as a whole shows its reason above the rows.
	if stepMark(detail[providers.StepCapabilities]) == theme.StatusError {
		msg := i18n.T(l, "wiz.state.failed")
		if k := detail[providers.StepCapabilities+providers.DetailMessage]; k != "" {
			msg = i18n.T(l, k)
		}
		caps = append(caps, fixedH(th, 34, w.text(label(th, 12, msg, th.Pal.Error))))
	}
	for _, c := range providerCaps {
		ds := stepMark(detail[providers.DetailCapPrefix+c])
		right := "—"
		switch ds {
		case theme.StatusOK:
			right = i18n.T(l, "wiz.byok.yes")
		case theme.StatusError:
			right = i18n.T(l, "wiz.byok.no")
		}
		caps = append(caps, checkLine(w, th, 34, ds, i18n.T(l, "wiz.byok.cap."+c), right))
	}
	divider := func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }
	panel := func(gtx layout.Context) layout.Dimensions {
		return surface(gtx, th, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			pad := func(top, bottom float32, w layout.Widget) layout.Widget {
				return func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: th.Dp(top), Bottom: th.Dp(bottom), Left: th.Dp(16), Right: th.Dp(16)}.Layout(gtx, w)
				}
			}
			return vstack(th, 0,
				pad(14, 14, spread(head, state)), divider,
				pad(6, 6, vstack(th, 0, steps...)), divider,
				pad(6, 14, vstack(th, 0, caps...)),
			)(gtx)
		})
	}
	var note layout.Widget
	if info != nil && info.Input.BaseURL != "" {
		txt := w.text(label(th, 12, i18n.Tf(l, "wiz.byok.privacy", hostOf(info.Input.BaseURL)), th.Pal.Muted))
		note = func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return box(gtx, th, 4, th.Pal.Surface, th.Pal.Divider, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(12), Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx, txt)
			})
		}
	}
	return vstack(th, 12, panel, note)
}

func (b *byokState) footer(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	w.mu.Lock()
	saved, saving := b.info != nil, b.saving
	w.mu.Unlock()
	enabled := w.cfg.Backends.Provider != nil && !saving && saved && !b.dirty && !s.running
	key := "wiz.check"
	if _, ok := s.result(readiness.StepBYOK); ok {
		key = "wiz.recheck"
	}
	f := footerSpec{
		primary: w.next(th, l, s),
		secondary: func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActTest, kit.ButtonStyle{Kind: kit.Secondary, Label: i18n.T(l, key)}, enabled)
		},
	}
	if b.dirty && saved && !s.running {
		f.status = i18n.T(l, "wiz.byok.saveFirst")
	}
	return f
}
