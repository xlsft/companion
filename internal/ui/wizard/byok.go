package wizard

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
)

// byokState is the provider form. Widgets are UI-goroutine only; the
// fields under "async" are guarded by Wizard.mu.
type byokState struct {
	kind                     widget.Enum
	tile                     widget.Enum // the chosen service: a preset's ID or customTile
	tileList                 widget.List
	custom                   bool // the custom server is picked: no preset matches on purpose
	lastSig                  string
	editedAt                 time.Time // the form last changed; it saves itself after a pause
	baseURL, model, key, hdr widget.Editor
	dirty                    bool // edited since the last save
	replacing                bool // the key editor is shown over a stored key
	showHeaders              bool
	models                   modelList

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
	b.models.reset(w)
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
		b.models.soon = true
	}
	if b.tile.Update(gtx) {
		i := slices.IndexFunc(providers.Presets, func(p providers.Preset) bool { return p.ID == b.tile.Value })
		if i < 0 {
			i = len(providers.Presets) // the custom server
		}
		b.pickPreset(gtx, i)
	} else if i := b.presetIndex(); i >= 0 {
		// Follow the form: a saved provider shows its tile.
		if i < len(providers.Presets) {
			b.tile.Value = providers.Presets[i].ID
		} else {
			b.tile.Value = customTile
		}
	}
	if b.kind.Update(gtx) {
		b.dirty = true
	}
	b.models.pick(w, gtx, &b.model)
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
	b.models.follow(w, gtx, b.modelQuery(w))
	b.autoSave(w, gtx, s)
}

// agentSaveAfter is the pause after the last edit before the form saves
// itself; there is no Save button.
const agentSaveAfter = 700 * time.Millisecond

// autoSave stores the form once it is complete and the user stopped
// typing; the connection check then runs by itself (autoSig).
func (b *byokState) autoSave(w *Wizard, gtx layout.Context, s snapshot) {
	sig := strings.Join([]string{b.kind.Value, b.baseURL.Text(), b.model.Text(), b.key.Text(), b.hdr.Text()}, "\x00")
	if sig != b.lastSig {
		b.lastSig, b.editedAt = sig, gtx.Now
	}
	w.mu.Lock()
	info, saving := b.info, b.saving
	w.mu.Unlock()
	if !b.dirty || saving || s.running || !b.complete(info) {
		return
	}
	if due := b.editedAt.Add(agentSaveAfter); gtx.Now.Before(due) {
		gtx.Execute(op.InvalidateCmd{At: due})
		return
	}
	b.save(w, gtx)
}

// complete reports a form that can be saved: an address, a model, and a
// key unless the endpoint takes none or one is stored.
func (b *byokState) complete(info *providers.Info) bool {
	if strings.TrimSpace(b.baseURL.Text()) == "" || strings.TrimSpace(b.model.Text()) == "" {
		return false
	}
	if strings.TrimSpace(b.key.Text()) != "" || (info != nil && info.HasKey) {
		return true
	}
	return !providers.NeedsKey(llm.Kind(b.kind.Value), b.baseURL.Text())
}

// modelQuery asks for the models of the endpoint as typed, once that is
// worth asking: a service needs its key first (typed, or stored for this
// host), a server of the user's own may take none.
func (b *byokState) modelQuery(w *Wizard) modelQuery {
	lister, ok := w.cfg.Backends.Provider.(ProviderModels)
	base := strings.TrimSpace(b.baseURL.Text())
	if !ok || !hasHost(base) {
		return modelQuery{}
	}
	hdr, err := parseHeaders(b.hdr.Text())
	if err != nil {
		return modelQuery{}
	}
	kind := llm.Kind(b.kind.Value)
	key := strings.TrimSpace(b.key.Text())
	w.mu.Lock()
	info, keyGen := b.info, b.keyGen
	w.mu.Unlock()
	id, stored := "", false
	if info != nil {
		id = info.ID
		stored = info.HasKey && info.Input.Kind == kind && sameHost(info.Input.BaseURL, base)
	}
	_, service := providers.PresetFor(kind, base)
	if key == "" && !stored && service && !localAddr(base) {
		return modelQuery{}
	}
	q := providers.ModelQuery{Kind: kind, BaseURL: base, Headers: hdr, APIKey: key, ProviderID: id}
	sig := strings.Join([]string{string(kind), base, formatHeaders(hdr), key, id, strconv.FormatBool(stored), keyGen}, "\x00")
	return modelQuery{sig: sig, fetch: func(ctx context.Context) ([]string, error) { return lister.ListModels(ctx, q) }}
}

// presetIndex is the dropdown's selection: the preset the endpoint matches,
// the "Other endpoint" row (len(Presets)) for anything else typed, or -1
// for an empty form.
func (b *byokState) presetIndex() int {
	if p, ok := providers.PresetFor(llm.Kind(b.kind.Value), b.baseURL.Text()); ok {
		return slices.IndexFunc(providers.Presets, func(x providers.Preset) bool { return x.ID == p.ID })
	}
	if b.custom || strings.TrimSpace(b.baseURL.Text()) != "" {
		return len(providers.Presets)
	}
	return -1
}

// pickPreset fills the form from preset i; len(Presets) is "Other
// endpoint", which empties a preset's address for the user's own. The
// model is always the user's choice: presets carry none.
func (b *byokState) pickPreset(gtx layout.Context, i int) {
	b.dirty = true
	if i >= len(providers.Presets) {
		if _, ok := providers.PresetFor(llm.Kind(b.kind.Value), b.baseURL.Text()); ok {
			b.baseURL.SetText("")
			changed(gtx, &b.baseURL)
		}
		b.custom = true
		gtx.Execute(key.FocusCmd{Tag: &b.baseURL})
		return
	}
	p := providers.Presets[i]
	b.models.soon = true
	b.custom = false
	b.kind.Value = string(p.Kind)
	b.baseURL.SetText(p.BaseURL)
	changed(gtx, &b.baseURL)
	if !p.Local {
		gtx.Execute(key.FocusCmd{Tag: &b.key})
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
		b.models.refresh()
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
