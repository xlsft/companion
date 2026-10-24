package wizard

import (
	"context"
	"math"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
)

// ttsState is the speech-output form. Widgets are UI-goroutine only; the
// async fields are guarded by Wizard.mu.
type ttsState struct {
	provider  widget.Enum
	key       widget.Editor
	speed     widget.Float
	voiceBtns []widget.Clickable
	dirty     bool
	replacing bool // the key editor is shown over a stored key

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
	if len(t.voiceBtns) < len(voices) {
		t.voiceBtns = append(t.voiceBtns, make([]widget.Clickable, len(voices)-len(t.voiceBtns))...)
	}
	for i := range voices {
		if t.voiceBtns[i].Clicked(gtx) {
			w.mu.Lock()
			t.voice = voices[i].ID
			w.mu.Unlock()
			t.dirty = true
		}
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
		w.mu.Unlock()
		// A new provider or key has other voices.
		if providerChanged || apiKey != "" {
			t.loadVoices(w)
		}
	})
}
