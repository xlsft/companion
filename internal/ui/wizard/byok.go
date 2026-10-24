package wizard

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// byokState is the provider form. Widgets are UI-goroutine only; the
// fields under "async" are guarded by Wizard.mu.
type byokState struct {
	kind                     widget.Enum
	baseURL, model, key, hdr widget.Editor
	dirty                    bool // edited since the last save

	// async
	loaded  bool
	prefill bool // info arrived, editors not yet filled
	info    *providers.Info
	saving  bool
	saveErr string
}

func (b *byokState) init() {
	b.kind.Value = string(llm.KindOpenAI)
	b.baseURL.SingleLine, b.model.SingleLine, b.key.SingleLine = true, true, true
	b.key.Mask = '•'
}

func (b *byokState) load(w *Wizard, _ context.Context) {
	if w.cfg.Backends.Provider == nil {
		return
	}
	w.goStep(func(ctx context.Context) {
		list, err := w.cfg.Backends.Provider.List(ctx)
		w.mu.Lock()
		defer w.mu.Unlock()
		if err != nil {
			b.saveErr = "load_failed"
			return
		}
		b.loaded = true
		if len(list) > 0 && b.info == nil {
			b.info, b.prefill = list[0], true
		}
	})
}

// parseHeaders reads "Name: value" lines; blank lines are ignored.
func parseHeaders(s string) (map[string]string, error) {
	h := map[string]string{}
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, errors.New("bad header line")
		}
		h[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if len(h) == 0 {
		return nil, nil
	}
	return h, nil
}

func formatHeaders(h map[string]string) string {
	var lines []string
	for k, v := range h {
		lines = append(lines, k+": "+v)
	}
	return strings.Join(slices.Sorted(slices.Values(lines)), "\n")
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	return u.Host
}

func (b *byokState) update(w *Wizard, gtx layout.Context, s snapshot) {
	w.mu.Lock()
	info, prefill := b.info, b.prefill
	b.prefill = false
	w.mu.Unlock()
	if prefill && info != nil {
		b.kind.Value = string(info.Input.Kind)
		b.baseURL.SetText(info.Input.BaseURL)
		b.model.SetText(info.Input.Model)
		b.hdr.SetText(formatHeaders(info.Input.Headers))
		for _, e := range []*widget.Editor{&b.baseURL, &b.model, &b.hdr} {
			changed(gtx, e)
		}
		b.dirty = false
	}
	if b.kind.Update(gtx) {
		b.dirty = true
	}
	for _, e := range []*widget.Editor{&b.baseURL, &b.model, &b.key, &b.hdr} {
		if changed(gtx, e) {
			b.dirty = true
		}
	}
	if w.clicked(gtx, ActSave) {
		b.save(w, gtx)
	}
	if w.clicked(gtx, ActTest) {
		w.runCheck(readiness.StepBYOK, nil, nil)
	}
}

// save submits the form. The key leaves the editor at once, so it is never
// on screen after Save, whatever the outcome.
func (b *byokState) save(w *Wizard, gtx layout.Context) {
	p := w.cfg.Backends.Provider
	w.mu.Lock()
	busy := b.saving
	w.mu.Unlock()
	if p == nil || busy {
		return
	}
	hdr, err := parseHeaders(b.hdr.Text())
	if err != nil {
		w.mu.Lock()
		b.saveErr = "bad_headers"
		w.mu.Unlock()
		return
	}
	base := strings.TrimSpace(b.baseURL.Text())
	in := providers.Input{
		Name:    hostOf(base),
		Kind:    llm.Kind(b.kind.Value),
		BaseURL: base,
		Model:   strings.TrimSpace(b.model.Text()),
		Headers: hdr,
		APIKey:  strings.TrimSpace(b.key.Text()),
	}
	b.key.SetText("")
	changed(gtx, &b.key)
	w.mu.Lock()
	b.saving, b.saveErr = true, ""
	id := ""
	if b.info != nil {
		id = b.info.ID
	}
	w.mu.Unlock()
	w.goStep(func(ctx context.Context) {
		var info *providers.Info
		var err error
		if id == "" {
			info, err = p.Create(ctx, in)
		} else {
			info, err = p.Update(ctx, id, in)
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		b.saving = false
		if err != nil {
			b.saveErr = saveErrCode(err)
			w.cfg.Log.Warn("setup provider save failed", "code", b.saveErr)
			return
		}
		b.info = info
	})
	b.dirty = false
}

func saveErrCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var ce *llm.ConfigError
	if errors.As(err, &ce) {
		return "invalid_" + strings.ToLower(ce.Field)
	}
	if k := llm.ErrorKind(err); k != "" && k != "unknown" {
		return k
	}
	return "save_failed"
}

func (b *byokState) rows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	w.mu.Lock()
	info, saving, saveErr, loaded := b.info, b.saving, b.saveErr, b.loaded
	w.mu.Unlock()
	enabled := w.cfg.Backends.Provider != nil && !saving && !s.running
	rows := []layout.Widget{
		w.text(kit.Muted(th, i18n.T(l, "wiz.byok.kind"))),
		column(
			material.RadioButton(th.Theme, &b.kind, string(llm.KindOpenAI), i18n.T(l, "wiz.byok.kind.openai")).Layout,
			material.RadioButton(th.Theme, &b.kind, string(llm.KindAnthropic), i18n.T(l, "wiz.byok.kind.anthropic")).Layout,
		),
		gap(10),
		field(w, th, i18n.T(l, "wiz.byok.baseURL"), "https://api.example.com/v1", &b.baseURL, enabled),
		field(w, th, i18n.T(l, "wiz.byok.model"), i18n.T(l, "wiz.byok.model.hint"), &b.model, enabled),
		field(w, th, i18n.T(l, "wiz.byok.key"), i18n.T(l, "wiz.byok.key.hint"), &b.key, enabled),
	}
	keyState := "wiz.byok.key.none"
	if info != nil && info.HasKey {
		keyState = "wiz.byok.key.saved"
	}
	rows = append(rows,
		w.text(th.StatusLabel(keyStatus(info), i18n.T(l, keyState))),
		gap(10),
		field(w, th, i18n.T(l, "wiz.byok.headers"), i18n.T(l, "wiz.byok.headers.hint"), &b.hdr, enabled),
	)
	if w.cfg.Backends.Provider == nil {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, i18n.T(l, "wiz.notWired"))))
	} else if !loaded && saveErr == "" {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.loading"))))
	}
	if saveErr != "" {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, i18n.Tf(l, "wiz.byok.saveFailed", saveErr))),
			w.text(kit.Muted(th, fixText(l, saveErr))))
	}
	if info != nil {
		rows = append(rows, gap(6), w.text(kit.Body(th, i18n.Tf(l, "wiz.byok.inUse", hostOf(info.Input.BaseURL), info.Input.Model))))
	}
	saved := info != nil
	rows = append(rows, gap(10), row(
		func(gtx layout.Context) layout.Dimensions {
			return w.button(gtx, th, ActSave, i18n.T(l, "wiz.save"), enabled)
		},
		func(gtx layout.Context) layout.Dimensions {
			return w.button(gtx, th, ActTest, i18n.T(l, "wiz.byok.test"), enabled && saved && !b.dirty)
		},
	))
	if b.dirty && saved {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.byok.saveFirst"))))
	}
	rows = append(rows, gap(12))
	rows = append(rows, b.testRows(w, th, l, s)...)
	return rows
}

func keyStatus(info *providers.Info) theme.Status {
	if info != nil && info.HasKey {
		return theme.StatusOK
	}
	return theme.StatusUnknown
}

// providerSteps are the provider test steps in order.
var providerSteps = []string{providers.StepConnection, providers.StepAuthModel, providers.StepCapabilities}

// providerCaps are the probed capabilities shown under the capability step.
var providerCaps = []string{"text", "stream", "vision", "tools"}

func providerStatus(l i18n.Lang, v string) (theme.Status, string) {
	switch providers.Status(v) {
	case providers.StatusOK:
		return theme.StatusOK, i18n.T(l, "wiz.state.passed")
	case providers.StatusFailed:
		return theme.StatusError, i18n.T(l, "wiz.state.failed")
	}
	return theme.StatusUnknown, i18n.T(l, "wiz.state.skipped")
}

// testRows renders the three provider test steps from Result.Detail.
func (b *byokState) testRows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	r, ok := s.result(readiness.StepBYOK)
	if !ok || r.Detail == nil {
		return nil
	}
	var rows []layout.Widget
	for _, id := range providerSteps {
		ds, word := providerStatus(l, r.Detail[id])
		line := i18n.T(l, "wiz.byok.step."+id) + " — " + word
		if c := r.Detail[id+providers.DetailCode]; c != "" {
			line += " (" + c + ")"
		}
		rows = append(rows, w.text(th.StatusLabel(ds, line)))
		if k := r.Detail[id+providers.DetailMessage]; k != "" {
			rows = append(rows, indent(w.text(kit.Muted(th, i18n.T(l, k)))))
		}
		if id == providers.StepCapabilities {
			for _, c := range providerCaps {
				ds, word := providerStatus(l, r.Detail[providers.DetailCapPrefix+c])
				rows = append(rows, indent(w.text(th.StatusLabel(ds, i18n.T(l, "wiz.byok.cap."+c)+" — "+word))))
			}
		}
	}
	return rows
}

func indent(wd layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 24}.Layout(gtx, wd)
	}
}
