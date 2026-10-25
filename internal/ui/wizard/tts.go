package wizard

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/kit"
)

// ttsState is the speech-output form. Widgets are UI-goroutine only; the
// async fields are guarded by Wizard.mu.
type ttsState struct {
	provider  widget.Enum
	preset    kit.Dropdown
	custom    bool // "Other endpoint" picked: no preset matches on purpose
	baseURL   widget.Editor
	model     widget.Editor
	key       widget.Editor
	speed     kit.Slider
	voiceEnum widget.Enum
	voiceDD   kit.Dropdown // the voice picker once there are too many for cards
	dirty     bool
	replacing bool // the key editor is shown over a stored key
	models    modelList

	// async
	loaded   bool
	prefill  bool
	cfg      TTSConfig
	saved    bool // cfg came from the backend
	voice    string
	voices   []tts.Voice
	loadingV bool
	saving   bool
	err      string
	saveGen  int // counts successful Saves; the speech check follows each
}

func (t *ttsState) init() {
	t.provider.Value = TTSOpenAI
	t.key.SingleLine, t.key.Mask = true, '•'
	t.baseURL.SingleLine, t.model.SingleLine = true, true
	t.speed.Float.Value = speedToSlider(tts.DefaultSpeed)
}

func speedToSlider(s float64) float32 {
	return float32((s - tts.MinSpeed) / (tts.MaxSpeed - tts.MinSpeed))
}

// sliderToSpeed snaps to 0.05 within [MinSpeed, MaxSpeed].
func sliderToSpeed(v float32) float64 {
	s := tts.MinSpeed + float64(v)*(tts.MaxSpeed-tts.MinSpeed)
	s = math.Round(s/0.05) * 0.05
	return math.Min(tts.MaxSpeed, math.Max(tts.MinSpeed, math.Round(s*100)/100))
}

func (t *ttsState) load(w *Wizard, _ context.Context) {
	b := w.cfg.Backends.TTS
	if b == nil {
		return
	}
	t.models.reset(w)
	w.goStep(func(ctx context.Context) {
		cfg, err := b.Config(ctx)
		w.mu.Lock()
		defer w.mu.Unlock()
		t.loaded = true
		if err != nil {
			t.err = "load_failed"
			return
		}
		if cfg.Provider != "" {
			t.cfg, t.saved, t.prefill, t.voice = cfg, true, true, cfg.Voice
		}
	})
	t.loadVoices(w)
}

func (t *ttsState) loadVoices(w *Wizard) {
	b := w.cfg.Backends.TTS
	w.mu.Lock()
	if b == nil || t.loadingV {
		w.mu.Unlock()
		return
	}
	t.loadingV = true
	w.mu.Unlock()
	w.goStep(func(ctx context.Context) {
		vs, err := b.Voices(ctx)
		w.mu.Lock()
		defer w.mu.Unlock()
		t.loadingV = false
		if err != nil {
			t.voices = nil
			return
		}
		t.voices = vs
	})
}

func (t *ttsState) update(w *Wizard, gtx layout.Context, s snapshot) {
	w.mu.Lock()
	cfg, prefill := t.cfg, t.prefill
	t.prefill = false
	w.mu.Unlock()
	if prefill {
		t.provider.Value = cfg.Provider
		t.baseURL.SetText(cfg.BaseURL)
		t.model.SetText(cfg.Model)
		changed(gtx, &t.baseURL)
		changed(gtx, &t.model)
		if cfg.Speed > 0 {
			t.speed.Float.Value = speedToSlider(cfg.Speed)
		}
		t.dirty = false
		t.models.soon = true
	}
	if i := t.preset.Update(gtx, len(tts.Presets)+1); i >= 0 {
		t.pickPreset(w, gtx, i)
	}
	if t.provider.Update(gtx) {
		t.dirty = true
		w.mu.Lock()
		t.voices, t.voice = nil, ""
		w.mu.Unlock()
	}
	t.models.pick(w, gtx, &t.model)
	for _, e := range []*widget.Editor{&t.baseURL, &t.model} {
		if changed(gtx, e) {
			t.dirty = true
		}
	}
	if changed(gtx, &t.key) {
		t.dirty = true
	}
	if moved, steps := t.speed.Update(gtx); moved || steps != 0 {
		if steps != 0 {
			// Keyboard steps move by the slider's 0.05 snap.
			s := sliderToSpeed(t.speed.Float.Value) + float64(steps)*0.05
			t.speed.Float.Value = speedToSlider(sliderToSpeed(speedToSlider(s)))
		}
		t.dirty = true
	}
	w.mu.Lock()
	voices := t.voices
	w.mu.Unlock()
	if i := t.voiceDD.Update(gtx, len(voices)); i >= 0 && i < len(voices) {
		w.mu.Lock()
		t.voice = voices[i].ID
		w.mu.Unlock()
		t.dirty = true
	}
	if t.voiceEnum.Update(gtx) {
		w.mu.Lock()
		t.voice = t.voiceEnum.Value
		w.mu.Unlock()
		t.dirty = true
	}
	if w.clicked(gtx, ActReplaceKey) {
		t.replacing = true
		gtx.Execute(key.FocusCmd{Tag: &t.key})
	}
	if w.clicked(gtx, ActLoadVoices) {
		t.loadVoices(w)
	}
	if w.clicked(gtx, ActSave) {
		t.save(w, gtx)
	}
	if w.checkClicked(gtx) {
		w.runCheck(readiness.StepTTS, nil, nil)
	}
	t.models.follow(w, gtx, t.modelQuery(w))
}

// modelQuery asks an OpenAI-compatible speech server for its models once
// that is worth asking: OpenAI needs its key first (typed or stored), a
// server of the user's own may take none. Other providers have no model.
func (t *ttsState) modelQuery(w *Wizard) modelQuery {
	lister, ok := w.cfg.Backends.TTS.(TTSModels)
	base := t.endpoint()
	if !ok || t.provider.Value != TTSOpenAI || base != "" && !hasHost(base) {
		return modelQuery{}
	}
	key := strings.TrimSpace(t.key.Text())
	w.mu.Lock()
	cfg := t.cfg
	w.mu.Unlock()
	stored := cfg.HasKey && cfg.Provider == TTSOpenAI && (cfg.BaseURL == base || sameHost(cfg.BaseURL, base))
	_, service := tts.PresetFor(TTSOpenAI, base)
	if key == "" && !stored && (base == "" || service && !localAddr(base)) {
		return modelQuery{}
	}
	q := TTSModelQuery{BaseURL: base, APIKey: key}
	sig := strings.Join([]string{base, key, strconv.FormatBool(stored)}, "\x00")
	return modelQuery{sig: sig, fetch: func(ctx context.Context) ([]string, error) { return lister.ListModels(ctx, q) }}
}

// endpoint is the form's Base URL, only for an OpenAI-compatible provider:
// ElevenLabs is its own service.
func (t *ttsState) endpoint() string {
	if t.provider.Value != TTSOpenAI {
		return ""
	}
	return strings.TrimSpace(t.baseURL.Text())
}

// presetIndex is the dropdown's selection: the matching preset, the
// "Other endpoint" row (len(tts.Presets)) for an address of the user's own,
// or -1.
func (t *ttsState) presetIndex() int {
	if p, ok := tts.PresetFor(t.provider.Value, t.endpoint()); ok && !t.custom {
		return slices.IndexFunc(tts.Presets, func(x tts.Preset) bool { return x.ID == p.ID })
	}
	if t.custom || t.endpoint() != "" {
		return len(tts.Presets)
	}
	return -1
}

// pickPreset fills the form from preset i; len(tts.Presets) is "Other
// endpoint". The model is the user's choice: presets carry none. Voices
// belong to a server, so they are reloaded after the next Save.
func (t *ttsState) pickPreset(w *Wizard, gtx layout.Context, i int) {
	t.dirty = true
	w.mu.Lock()
	t.voices, t.voice = nil, ""
	w.mu.Unlock()
	if i >= len(tts.Presets) {
		t.custom = true
		t.provider.Value = TTSOpenAI
		if _, ok := tts.PresetFor(TTSOpenAI, t.endpoint()); ok {
			t.baseURL.SetText("")
			changed(gtx, &t.baseURL)
		}
		gtx.Execute(key.FocusCmd{Tag: &t.baseURL})
		return
	}
	p := tts.Presets[i]
	t.models.soon = true
	t.custom = false
	t.provider.Value = p.Provider
	t.baseURL.SetText(p.BaseURL)
	changed(gtx, &t.baseURL)
	if !p.Local {
		gtx.Execute(key.FocusCmd{Tag: &t.key})
	}
}

// keyless reports a saved endpoint of the user's own, which may take no
// key; voices can be listed without one.
func keyless(cfg TTSConfig) bool { return !tts.NeedsKey(cfg.Provider, cfg.BaseURL) }

// save stores the form; the key leaves the editor at once.
func (t *ttsState) save(w *Wizard, gtx layout.Context) {
	b := w.cfg.Backends.TTS
	w.mu.Lock()
	if b == nil || t.saving {
		w.mu.Unlock()
		return
	}
	model := ""
	if t.provider.Value == TTSOpenAI {
		model = strings.TrimSpace(t.model.Text())
	}
	providerChanged := t.cfg.Provider != t.provider.Value || t.cfg.BaseURL != t.endpoint() || t.cfg.Model != model
	cfg := TTSConfig{Provider: t.provider.Value, BaseURL: t.endpoint(), Model: model, Voice: t.voice, Speed: sliderToSpeed(t.speed.Float.Value)}
	t.saving, t.err = true, ""
	w.mu.Unlock()
	apiKey := strings.TrimSpace(t.key.Text())
	t.key.SetText("")
	changed(gtx, &t.key)
	t.replacing = false
	t.dirty = false
	w.goStep(func(ctx context.Context) {
		out, err := b.Save(ctx, cfg, apiKey)
		w.mu.Lock()
		t.saving = false
		if err != nil {
			t.err = "save_failed"
			w.mu.Unlock()
			w.cfg.Log.Warn("setup tts save failed", "code", "save_failed")
			return
		}
		t.cfg, t.saved, t.voice = out, true, out.Voice
		t.saveGen++
		t.models.refresh()
		w.mu.Unlock()
		// A new provider or key has other voices.
		if providerChanged || apiKey != "" {
			t.loadVoices(w)
		}
	})
}

// autoSig is non-empty only after the user saved in this session: the
// check plays a sentence, which is expected right after Save and a surprise
// on merely opening the step.
func (t *ttsState) autoSig(w *Wizard) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.saveGen == 0 || t.saving {
		return "", false
	}
	return strconv.Itoa(t.saveGen), true
}
