package wizard

import (
	"context"
	"math"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// ttsState is the speech-output form. Widgets are UI-goroutine only; the
// async fields are guarded by Wizard.mu.
type ttsState struct {
	provider widget.Enum
	key      widget.Editor
	speed    widget.Float
	voiceDD  dropdown
	dirty    bool

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
}

func (t *ttsState) init() {
	t.provider.Value = TTSOpenAI
	t.key.SingleLine, t.key.Mask = true, '•'
	t.speed.Value = speedToSlider(tts.DefaultSpeed)
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
	cfg, prefill, voices := t.cfg, t.prefill, t.voices
	t.prefill = false
	w.mu.Unlock()
	if prefill {
		t.provider.Value = cfg.Provider
		if cfg.Speed > 0 {
			t.speed.Value = speedToSlider(cfg.Speed)
		}
		t.dirty = false
	}
	if t.provider.Update(gtx) {
		t.dirty = true
		w.mu.Lock()
		t.voices, t.voice = nil, ""
		w.mu.Unlock()
	}
	if changed(gtx, &t.key) {
		t.dirty = true
	}
	if t.speed.Update(gtx) {
		t.dirty = true
	}
	if i := t.voiceDD.update(gtx, len(voices)); i >= 0 {
		w.mu.Lock()
		t.voice = voices[i].ID
		w.mu.Unlock()
		t.dirty = true
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
}

// save stores the form; the key leaves the editor at once.
func (t *ttsState) save(w *Wizard, gtx layout.Context) {
	b := w.cfg.Backends.TTS
	w.mu.Lock()
	if b == nil || t.saving {
		w.mu.Unlock()
		return
	}
	providerChanged := t.cfg.Provider != t.provider.Value
	cfg := TTSConfig{Provider: t.provider.Value, Voice: t.voice, Speed: sliderToSpeed(t.speed.Value)}
	t.saving, t.err = true, ""
	w.mu.Unlock()
	key := strings.TrimSpace(t.key.Text())
	t.key.SetText("")
	changed(gtx, &t.key)
	t.dirty = false
	w.goStep(func(ctx context.Context) {
		out, err := b.Save(ctx, cfg, key)
		w.mu.Lock()
		t.saving = false
		if err != nil {
			t.err = "save_failed"
			w.mu.Unlock()
			w.cfg.Log.Warn("setup tts save failed", "code", "save_failed")
			return
		}
		t.cfg, t.saved, t.voice = out, true, out.Voice
		w.mu.Unlock()
		// A new provider or key has other voices.
		if providerChanged || key != "" {
			t.loadVoices(w)
		}
	})
}

func (t *ttsState) rows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	b := w.cfg.Backends.TTS
	w.mu.Lock()
	loaded, cfg, saved, voice, voices := t.loaded, t.cfg, t.saved, t.voice, t.voices
	loadingV, saving, errCode := t.loadingV, t.saving, t.err
	w.mu.Unlock()
	enabled := b != nil && !saving && !s.running
	rows := []layout.Widget{
		w.text(kit.Muted(th, i18n.T(l, "wiz.tts.provider"))),
		column(
			material.RadioButton(th.Theme, &t.provider, TTSOpenAI, i18n.T(l, "wiz.tts.provider.openai")).Layout,
			material.RadioButton(th.Theme, &t.provider, TTSElevenLabs, i18n.T(l, "wiz.tts.provider.elevenlabs")).Layout,
		),
		gap(10),
		field(w, th, i18n.T(l, "wiz.tts.key"), i18n.T(l, "wiz.byok.key.hint"), &t.key, enabled),
	}
	keyState, ks := "wiz.byok.key.none", theme.StatusUnknown
	if cfg.HasKey {
		keyState, ks = "wiz.byok.key.saved", theme.StatusOK
	}
	rows = append(rows, w.text(th.StatusLabel(ks, i18n.T(l, keyState))), gap(10))

	labels := make([]string, len(voices))
	sel := -1
	for i, v := range voices {
		labels[i] = kit.OrDash(v.Name)
		if len(v.Languages) > 0 {
			labels[i] += " · " + strings.Join(v.Languages, ", ")
		}
		if v.ID == voice {
			sel = i
		}
	}
	placeholder := i18n.T(l, "wiz.tts.pickVoice")
	if loadingV {
		placeholder = i18n.T(l, "wiz.loading")
	} else if len(voices) == 0 && voice != "" {
		placeholder = voice
	}
	rows = append(rows,
		w.text(kit.Muted(th, i18n.T(l, "wiz.tts.voice"))),
		row(
			func(gtx layout.Context) layout.Dimensions {
				return t.voiceDD.layout(w, gtx, th, labels, sel, placeholder, enabled)
			},
			func(gtx layout.Context) layout.Dimensions {
				return w.button(gtx, th, ActLoadVoices, i18n.T(l, "wiz.tts.loadVoices"), enabled && cfg.HasKey && !loadingV)
			},
		),
		gap(10),
		w.text(kit.Muted(th, i18n.Tf(l, "wiz.tts.speed", sliderToSpeed(t.speed.Value)))),
		func(gtx layout.Context) layout.Dimensions {
			if !enabled {
				gtx = gtx.Disabled()
			}
			gtx.Constraints.Min.X = min(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(360*th.Scale)))
			gtx.Constraints.Max.X = gtx.Constraints.Min.X
			return material.Slider(th.Theme, &t.speed).Layout(gtx)
		},
		gap(10),
	)
	if b == nil {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, i18n.T(l, "wiz.notWired"))))
	} else if !loaded {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.loading"))))
	}
	if errCode != "" {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, errCode)), w.text(kit.Muted(th, fixText(l, errCode))))
	}
	sentence := w.text(kit.Body(th, "«"+readiness.TTSSentence(string(l))+"»"))
	rows = append(rows,
		row(
			func(gtx layout.Context) layout.Dimensions {
				return w.button(gtx, th, ActSave, i18n.T(l, "wiz.save"), enabled)
			},
			w.checkButton(th, l, s, readiness.StepTTS, saved && !t.dirty),
		),
		gap(8),
		w.text(kit.Muted(th, i18n.T(l, "wiz.tts.plays"))),
		func(gtx layout.Context) layout.Dimensions { return kit.Card(gtx, th, sentence) },
	)
	if t.dirty && saved {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.byok.saveFirst"))))
	}
	return rows
}
