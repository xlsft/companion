package wizard

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
)

// byokState is the provider form. Widgets are UI-goroutine only; the
// fields under "async" are guarded by Wizard.mu.
type byokState struct {
	kind                     widget.Enum
	baseURL, model, key, hdr widget.Editor
	dirty                    bool // edited since the last save
	replacing                bool // the key editor is shown over a stored key
	showHeaders              bool

	// async
	loaded  bool
	prefill bool // info arrived, editors not yet filled
	info    *providers.Info
	saving  bool
	saveErr string
	keyGen  string // changes when a new key was stored
	keyN    int
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
		b.showHeaders = b.showHeaders || len(info.Input.Headers) > 0
	}
	if b.kind.Update(gtx) {
		b.dirty = true
	}
	for _, e := range []*widget.Editor{&b.baseURL, &b.model, &b.key, &b.hdr} {
		if changed(gtx, e) {
			b.dirty = true
		}
	}
	if w.clicked(gtx, ActReplaceKey) {
		b.replacing = true
		gtx.Execute(key.FocusCmd{Tag: &b.key})
	}
	if w.clicked(gtx, ActHeaders) {
		b.showHeaders = !b.showHeaders
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
	b.replacing = false
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
		if in.APIKey != "" {
			b.keyN++
			b.keyGen = strconv.Itoa(b.keyN)
		}
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

// autoSig identifies the saved provider the check should cover; ok is false
// until one is saved and the form has no unsaved edits. A changed saved
// provider changes the signature and so re-runs the check.
func (b *byokState) autoSig(w *Wizard) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b.info == nil || b.saving || b.dirty || !b.info.HasKey {
		return "", false
	}
	in := b.info.Input
	return strings.Join([]string{b.info.ID, string(in.Kind), in.BaseURL, in.Model, formatHeaders(in.Headers), b.keyGen}, "\x00"), true
}
