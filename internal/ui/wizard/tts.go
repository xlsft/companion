package wizard

import (
	"context"
	"math"
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
	key       widget.Editor
	speed     kit.Slider
	voiceEnum widget.Enum
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
	saveGen  int // counts successful Saves; the speech check follows each
}

func (t *ttsState) init() {
	t.provider.Value = TTSOpenAI
	t.key.SingleLine, t.key.Mask = true, '•'
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
		if cfg.Speed > 0 {
			t.speed.Float.Value = speedToSlider(cfg.Speed)
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
	if moved, steps := t.speed.Update(gtx); moved || steps != 0 {
		if steps != 0 {
			// Keyboard steps move by the slider's 0.05 snap.
			s := sliderToSpeed(t.speed.Float.Value) + float64(steps)*0.05
			t.speed.Float.Value = speedToSlider(sliderToSpeed(speedToSlider(s)))
		}
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
	cfg := TTSConfig{Provider: t.provider.Value, Voice: t.voice, Speed: sliderToSpeed(t.speed.Float.Value)}
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
