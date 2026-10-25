package wizard

import (
	"context"
	"strconv"
	"strings"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// The voice screen: the voice answers are spoken with, the microphone,
// and one check where Companion says the phrase and the microphone hears
// it (StepTTS, then StepSTT on the same recording).

// voiceOrder is the voice tiles' order: Edge first, as it needs nothing.
var voiceOrder = []string{tts.EdgeProviderID, "openai", "elevenlabs", "openrouter", "kokoro", "speaches"}

var voiceLogos = map[string]icon.Name{"openai": icon.BrandOpenAI, "elevenlabs": icon.BrandElevenLabs, "openrouter": icon.BrandOpenRouter}

var voiceMarks = map[string]string{tts.EdgeProviderID: "E", "kokoro": "K", "speaches": "S", customTile: "+"}

func voiceTiles(l i18n.Lang) ([]string, []kit.Tile) {
	var keys []string
	var tiles []kit.Tile
	for _, id := range voiceOrder {
		p, ok := presetByID(id)
		if !ok {
			continue
		}
		t := kit.Tile{Logo: voiceLogos[id], Mark: voiceMarks[id], Title: p.Name}
		switch {
		case id == tts.EdgeProviderID:
			t.Sub, t.Badge = i18n.T(l, "wiz.tile.free"), i18n.T(l, "wiz.tile.default")
		case p.Local:
			t.Sub = i18n.T(l, "wiz.tile.local")
		default:
			t.Sub = i18n.T(l, "wiz.tile.needsKey")
		}
		keys, tiles = append(keys, id), append(tiles, t)
	}
	keys = append(keys, customTile)
	tiles = append(tiles, kit.Tile{Mark: voiceMarks[customTile], Title: i18n.T(l, "wiz.tile.custom"), Sub: i18n.T(l, "wiz.tile.custom.d")})
	return keys, tiles
}

func presetByID(id string) (tts.Preset, bool) {
	for _, p := range tts.Presets {
		if p.ID == id {
			return p, true
		}
	}
	return tts.Preset{}, false
}

// voiceSig is what the automatic voice check covers; ok is false until it
// can run: the speech model is ready and the voice is saved.
func (w *Wizard) voiceSig() (string, bool) {
	if phase, _, _ := w.model.state(); phase != modelReady {
		return "", false
	}
	t := &w.tts
	w.mu.Lock()
	defer w.mu.Unlock()
	if !t.loaded || t.saving || t.dirty {
		return "", false
	}
	c := t.cfg
	return strings.Join([]string{c.Provider, c.BaseURL, c.Model, c.Voice, w.stt.device, strconv.Itoa(t.saveGen)}, "\x00"), true
}

// voiceCheck speaks the phrase and listens for it (StepTTS); when the
// microphone heard it, StepSTT passes on that same recording at once.
func (w *Wizard) voiceCheck() {
	b := w.cfg.Backends.STT
	w.mu.Lock()
	dev := w.stt.device
	w.stt.transcript = ""
	w.mu.Unlock()
	w.runCheck(readiness.StepTTS, func(ctx context.Context) error {
		if b == nil {
			return nil
		}
		return b.SelectDevice(ctx, dev)
	}, func(ctx context.Context) {
		r := w.cfg.Gate.Progress().Results[readiness.StepTTS]
		if r.Status == readiness.Passed && r.Detail[readiness.DetailHeard] == readiness.DetailOK {
			if _, err := w.cfg.Gate.Run(ctx, readiness.StepSTT); err != nil {
				w.cfg.Log.Warn("setup voice check: recognition step did not run", "code", runErrCode(err))
			}
		}
		if b != nil {
			tr := b.LastTranscript()
			w.mu.Lock()
			w.stt.transcript = tr
			w.mu.Unlock()
		}
	})
}

func (w *Wizard) voiceUpdate(gtx layout.Context, s snapshot) {
	if !s.running {
		w.stt.reading = false
	}
	if w.clicked(gtx, ActVoiceCheck) && !s.running {
		w.voiceCheck()
	}
	if w.clicked(gtx, ActReadSelf) && !s.running {
		w.stt.reading = true
		w.stt.check(w)
	}
}

func (w *Wizard) voiceBody(th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	t := &w.tts
	w.mu.Lock()
	cfg, voice, voices, loadingV, saving := t.cfg, t.voice, t.voices, t.loadingV, t.saving
	devs, dev := w.stt.devices, w.stt.device
	w.mu.Unlock()
	enabled := w.cfg.Backends.TTS != nil && !saving && !s.running
	keys, tiles := voiceTiles(l)
	rows := []layout.Widget{w.tileGrid(th, &t.tile, &t.tileList, keys, tiles, 2, enabled)}

	// Only what the chosen voice needs: a key for a hosted service, the
	// address and model for a server of the user's own.
	var form []layout.Widget
	if t.provider.Value == TTSOpenAI && (t.custom || t.endpoint() != "") {
		if t.custom {
			form = append(form, field(w, th, i18n.T(l, "wiz.byok.baseURL"), "http://localhost:8880/v1", &t.baseURL, enabled))
		}
		form = append(form, t.models.field(w, th, l, i18n.T(l, "wiz.byok.model.hint"), &t.model, enabled))
	}
	if tts.NeedsKey(t.provider.Value, t.endpoint()) {
		keyField := field(w, th, i18n.T(l, "wiz.tts.key"), i18n.T(l, "wiz.byok.key.hint"), &t.key, enabled)
		if cfg.HasKey && cfg.Provider == t.provider.Value && !t.replacing {
			keyField = storedKey(w, th, l, i18n.T(l, "wiz.tts.key"), enabled)
		}
		form = append(form, keyField)
	}
	if len(form) > 0 {
		rows = append(rows, kit.VStack(th, 14, form...))
	}
	rows = append(rows, kit.Grid(th, 2, 12,
		w.voicePicker(th, l, voices, voice, loadingV, enabled),
		w.micPicker(th, l, devs, dev, enabled)))
	rows = append(rows, w.voiceCard(th, l, s))
	return stepBody{rows: rows}
}

// voicePicker is the voice of the chosen service; "" is its default.
func (w *Wizard) voicePicker(th *theme.Theme, l i18n.Lang, voices []tts.Voice, voice string, loading, enabled bool) layout.Widget {
	label := w.label(i18n.T(l, "wiz.tts.voice"))
	opts := make([]kit.RichOption, len(voices))
	sel := -1
	for i, v := range voices {
		opts[i] = kit.RichOption{Title: kit.OrDash(v.Name), Desc: strings.Join(v.Languages, ", ")}
		if v.ID == voice {
			sel = i
		}
	}
	hint := i18n.T(l, "wiz.voice.default")
	if loading {
		hint = i18n.T(l, "wiz.loading")
	}
	if sel >= 0 {
		w.frame.texts = append(w.frame.texts, opts[sel].Title)
	} else {
		w.frame.texts = append(w.frame.texts, hint)
	}
	w.frame.actions = append(w.frame.actions, ActChoose)
	return kit.Labeled(th, label, func(gtx layout.Context) layout.Dimensions {
		if !enabled || len(voices) == 0 {
			gtx = gtx.Disabled()
		}
		return w.tts.voiceDD.LayoutRich(gtx, th, opts, sel, hint)
	})
}

// micPicker is the microphone; the first row is the system default.
func (w *Wizard) micPicker(th *theme.Theme, l i18n.Lang, devs []audio.Device, dev string, enabled bool) layout.Widget {
	label := w.label(i18n.T(l, "wiz.stt.device"))
	// Rich like the voice picker beside it, so the two fields line up.
	opts := []kit.RichOption{{Title: i18n.T(l, "wiz.stt.defaultDevice")}}
	sel := 0
	for i, d := range devs {
		opts = append(opts, kit.RichOption{Title: d.Name})
		if d.ID == dev {
			sel = i + 1
		}
	}
	return kit.Labeled(th, label, func(gtx layout.Context) layout.Dimensions {
		return w.richDropdown(gtx, th, &w.stt.deviceDD, opts, sel, opts[0].Title, enabled)
	})
}

// voiceCard is the check: what was said, what was heard, and what to do
// when the microphone did not hear it.
func (w *Wizard) voiceCard(th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	phrase := readiness.MicPhrase(string(l))
	w.mu.Lock()
	heardText := w.stt.transcript
	w.mu.Unlock()
	phase, _, _ := w.model.state()
	ttsR, ttsOK := s.result(readiness.StepTTS)
	sttR, sttOK := s.result(readiness.StepSTT)

	act, key := ActVoiceCheck, "wiz.voice.check"
	if ttsOK {
		key = "wiz.voice.checkAgain"
	}
	btnEnabled := !s.running && phase == modelReady
	if phase != modelReady {
		key = "wiz.voice.waitModel"
	}
	head := kit.Spread(
		w.text(kit.Medium(kit.Text(th, 14, i18n.T(l, "wiz.voice.title"), th.Pal.Fg))),
		func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, act, kit.ButtonStyle{Kind: kit.Secondary, Small: true, Icon: icon.Refresh2, Label: i18n.T(l, key)}, btnEnabled)
		})
	lines := []layout.Widget{head}
	spoke := ttsOK && ttsR.Detail[readiness.DetailSpoke] == readiness.DetailOK
	heard := ""
	if ttsOK {
		heard = ttsR.Detail[readiness.DetailHeard]
	}
	switch {
	case s.running && w.stt.reading:
		lines = append(lines, statusText(w, th, theme.StatusUnknown, i18n.Tf(l, "wiz.voice.readNow", phrase)))
	case s.running:
		lines = append(lines, statusText(w, th, theme.StatusUnknown, i18n.Tf(l, "wiz.voice.running", phrase)))
	case ttsOK && ttsR.Status == readiness.Failed:
		lines = append(lines, statusText(w, th, theme.StatusError, fixText(l, ttsR.Code)))
	case spoke:
		lines = append(lines, statusText(w, th, theme.StatusOK, i18n.Tf(l, "wiz.voice.said", phrase)))
		switch {
		case sttOK && sttR.Status == readiness.Passed:
			if heardText != "" {
				lines = append(lines, statusText(w, th, theme.StatusOK, i18n.Tf(l, "wiz.voice.heard", heardText)))
			}
			lines = append(lines, statusText(w, th, theme.StatusOK, i18n.T(l, "wiz.voice.works")))
		default:
			why := i18n.T(l, "wiz.voice.notHeard")
			if heard == readiness.HeardMismatch && heardText != "" {
				why = i18n.Tf(l, "wiz.voice.heardWrong", heardText)
			} else if heard != readiness.HeardNoSpeech && heard != readiness.DetailOK && heard != "" {
				why = fixText(l, heard)
			}
			if sttOK && sttR.Status != readiness.Passed {
				why = fixText(l, sttR.Code)
			}
			lines = append(lines, statusText(w, th, theme.StatusError, why),
				kit.HStack(th, 8,
					w.text(kit.Muted(th, i18n.T(l, "wiz.voice.headphones"))),
					func(gtx layout.Context) layout.Dimensions {
						return w.btn(gtx, th, ActReadSelf, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.voice.readSelf")}, !s.running)
					}))
		}
	case phase != modelReady:
		lines = append(lines, statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.voice.afterModel")))
	}
	return panel(th, kit.VStack(th, 12, lines...))
}
