package wizard

import (
	"gioui.org/layout"

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
		kit.Columns(th, 360, 40, 840, b.form(w, th, l, info, enabled, loaded, saveErr), b.side(w, th, l, s, info)),
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
			return kit.RadioCard(gtx, th, &b.kind, string(k), title, sub)
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
		status = alert(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"), "")
	case saveErr != "":
		status = alert(w, th, theme.StatusError, i18n.Tf(l, "wiz.byok.saveFailed", saveErr), fixText(l, saveErr))
	case !loaded:
		status = w.text(kit.Muted(th, i18n.T(l, "wiz.loading")))
	}
	return kit.VStack(th, 22,
		kit.Flow(th, 220, 8, kind(llm.KindAnthropic), kind(llm.KindOpenAI)),
		kit.VStack(th, 14,
			field(w, th, i18n.T(l, "wiz.byok.baseURL"), "https://api.example.com/v1", &b.baseURL, enabled),
			kit.Flow(th, 200, 12,
				field(w, th, i18n.T(l, "wiz.byok.model"), i18n.T(l, "wiz.byok.model.hint"), &b.model, enabled),
				keyField),
			headers,
		),
		kit.Spread(func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActHeaders, hdrB, enabled)
		}, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActSave, kit.ButtonStyle{Kind: kit.Secondary, Label: i18n.T(l, "wiz.save")}, enabled)
		}),
		status,
	)
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

// checkLine is a recorded kit.CheckLine; its accessible text is
// "name — detail".
func checkLine(w *Wizard, th *theme.Theme, h float32, ds theme.Status, name, right string) layout.Widget {
	w.frame.texts = append(w.frame.texts, name+" — "+right)
	return kit.CheckLine(th, h, ds, name, right)
}

// side is the connection check panel and the privacy note.
func (b *byokState) side(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot, info *providers.Info) layout.Widget {
	r, ok := s.result(readiness.StepBYOK)
	detail := map[string]string{}
	if ok && r.Detail != nil {
		detail = r.Detail
	}
	head := w.text(kit.Subheading(th, i18n.T(l, "wiz.byok.panel")))
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
		caps = append(caps, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(8), Bottom: th.Dp(4)}.Layout(gtx, alert(w, th, theme.StatusError, msg, ""))
		})
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
	pad := func(top, bottom float32, wd layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(top), Bottom: th.Dp(bottom), Left: th.Dp(16), Right: th.Dp(16)}.Layout(gtx, wd)
		}
	}
	panel := rows(th,
		pad(14, 14, kit.Spread(head, state)),
		pad(6, 6, kit.VStack(th, 0, steps...)),
		pad(6, 14, kit.VStack(th, 0, caps...)),
	)
	var note layout.Widget
	if info != nil && info.Input.BaseURL != "" {
		note = card(th, w.text(kit.Muted(th, i18n.Tf(l, "wiz.byok.privacy", hostOf(info.Input.BaseURL)))))
	}
	return kit.VStack(th, 12, panel, note)
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
