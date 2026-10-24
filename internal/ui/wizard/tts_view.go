package wizard

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

func (t *ttsState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	b := w.cfg.Backends.TTS
	w.mu.Lock()
	loaded, cfg, voice, voices := t.loaded, t.cfg, t.voice, t.voices
	loadingV, saving, errCode := t.loadingV, t.saving, t.err
	w.mu.Unlock()
	enabled := b != nil && !saving && !s.running

	prov := func(id string) layout.Widget {
		title, sub := i18n.T(l, "wiz.tts.provider."+id), i18n.T(l, "wiz.tts.provider."+id+".d")
		w.frame.texts = append(w.frame.texts, title, sub)
		return func(gtx layout.Context) layout.Dimensions {
			if !enabled {
				gtx = gtx.Disabled()
			}
			return kit.RadioCard(gtx, th, &t.provider, id, title, sub)
		}
	}
	keyField := field(w, th, i18n.T(l, "wiz.tts.key"), i18n.T(l, "wiz.byok.key.hint"), &t.key, enabled)
	if cfg.HasKey && !t.replacing {
		keyField = storedKey(w, th, l, i18n.T(l, "wiz.tts.key"), enabled)
	}
	keyRow := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.End}.Layout(gtx,
			layout.Flexed(1, keyField),
			kit.HSpace(th.Dp(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return w.btn(gtx, th, ActSave, kit.ButtonStyle{Kind: kit.Secondary, Label: i18n.T(l, "wiz.save")}, enabled)
			}),
		)
	}

	var status layout.Widget
	switch {
	case b == nil:
		status = alert(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"), "")
	case errCode != "":
		status = alert(w, th, theme.StatusError, errCode, fixText(l, errCode))
	case !loaded:
		status = w.text(kit.Muted(th, i18n.T(l, "wiz.loading")))
	}

	return stepBody{rows: []layout.Widget{
		kit.Flow(th, 220, 8, prov(TTSElevenLabs), prov(TTSOpenAI)),
		kit.VStack(th, 8, keyRow, status),
		t.voicesBlock(w, th, l, voices, voice, loadingV, enabled && cfg.HasKey && !loadingV),
		func(gtx layout.Context) layout.Dimensions {
			sp, ph := t.speedCard(w, th, l, enabled), t.phraseCard(w, th, l, s)
			if gtx.Constraints.Max.X < gtx.Dp(th.Dp(560)) {
				return kit.VStack(th, 16, sp, ph)(gtx)
			}
			return kit.Grid(th, 2, 16, sp, ph)(gtx)
		},
	}}
}

// storedKey stands in for a key field once a key is stored: the key never
// comes back to the UI, only the fact that it exists. Replace shows the
// editor again.
func storedKey(w *Wizard, th *theme.Theme, l i18n.Lang, title string, enabled bool) layout.Widget {
	w.frame.texts = append(w.frame.texts, title)
	saved := kit.Text(th, 14, i18n.T(l, "wiz.byok.key.saved"), th.Pal.Muted)
	saved.MaxLines = 1
	txt := w.text(saved)
	return kit.Labeled(th, title, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Box(gtx, th, th.Pal.Raised, th.Pal.Divider, 4, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(12), Right: th.Dp(3)}.Layout(gtx, kit.FixedH(th, 36, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(kit.IconW(th, icon.Lock, 14, th.Pal.Muted)),
					kit.HSpace(th.Dp(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = 0
						return txt(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return w.btn(gtx, th, ActReplaceKey, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.byok.key.replace")}, enabled)
					}),
				)
			}))
		})
	})
}

func (t *ttsState) voicesBlock(w *Wizard, th *theme.Theme, l i18n.Lang, voices []tts.Voice, voice string, loading, canLoad bool) layout.Widget {
	head := kit.Spread(w.text(kit.Caption(th, i18n.T(l, "wiz.tts.voice"))), func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, ActLoadVoices, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Icon: icon.Refresh2, Label: i18n.T(l, "wiz.tts.loadVoices")}, canLoad)
	})
	if len(voices) == 0 {
		msg := i18n.T(l, "wiz.tts.noVoices")
		if loading {
			msg = i18n.T(l, "wiz.loading")
		} else if voice != "" {
			msg = voice
		}
		return kit.VStack(th, 8, head, w.text(kit.Muted(th, msg)))
	}
	// Voices are a radio group like the providers above: the same card.
	t.voiceEnum.Value = voice
	tiles := make([]layout.Widget, len(voices))
	for i, v := range voices {
		name := kit.OrDash(v.Name)
		sub := strings.ToUpper(strings.Join(v.Languages, ", "))
		w.frame.texts = append(w.frame.texts, name)
		tiles[i] = func(gtx layout.Context) layout.Dimensions {
			w.frame.actions = append(w.frame.actions, ActChoose)
			return kit.RadioCard(gtx, th, &t.voiceEnum, v.ID, name, sub)
		}
	}
	return func(gtx layout.Context) layout.Dimensions {
		cols := 4
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(560)) {
			cols = 2
		}
		return kit.VStack(th, 8, head, kit.Grid(th, cols, 8, tiles...))(gtx)
	}
}

func (t *ttsState) speedCard(w *Wizard, th *theme.Theme, l i18n.Lang, enabled bool) layout.Widget {
	title := w.text(kit.Subheading(th, i18n.T(l, "wiz.tts.speed")))
	v := kit.Code(th, fmt.Sprintf("%.2f×", sliderToSpeed(t.speed.Float.Value)))
	v.LineHeightScale = 1
	w.frame.texts = append(w.frame.texts, v.Text)
	ticks := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		tick := func(s float64) layout.FlexChild {
			return layout.Rigid(kit.Caption(th, fmt.Sprintf("%.1f×", s)).Layout)
		}
		return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
			tick(tts.MinSpeed), tick((tts.MinSpeed+tts.MaxSpeed)/2), tick(tts.MaxSpeed))
	}
	slider := func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		return t.speed.Layout(gtx, th, i18n.T(l, "wiz.tts.speed"))
	}
	return card(th, kit.VStack(th, 10, kit.Spread(title, v.Layout), slider, ticks))
}

func (t *ttsState) phraseCard(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	title := w.text(kit.Subheading(th, i18n.T(l, "wiz.tts.phrase")))
	var badge layout.Widget
	if r, ok := s.result(readiness.StepTTS); ok && r.Status == readiness.Passed && !s.running {
		word := i18n.T(l, "wiz.tts.played")
		w.frame.texts = append(w.frame.texts, word)
		badge = func(gtx layout.Context) layout.Dimensions { return kit.Badge(gtx, th, theme.StatusOK, word) }
	}
	sentence := kit.Text(th, 14, "«"+readiness.TTSSentence(string(l))+"»", th.Pal.Fg2)
	sentence.LineHeightScale = 1.5
	return card(th, kit.VStack(th, 10, kit.Spread(title, badge), w.text(sentence),
		w.text(kit.Muted(th, i18n.T(l, "wiz.tts.plays")))))
}

func (t *ttsState) footer(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	w.mu.Lock()
	saved, saving := t.saved, t.saving
	w.mu.Unlock()
	act, key := ActCheck, "wiz.tts.listen"
	if _, ok := s.result(readiness.StepTTS); ok {
		act, key = ActRecheck, "wiz.tts.listenAgain"
	}
	ok := w.cfg.Backends.TTS != nil && saved && !t.dirty && !saving && !s.running
	f := footerSpec{
		secondary: func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, act, kit.ButtonStyle{Kind: kit.Secondary, Icon: icon.PlayFill, Label: i18n.T(l, key)}, ok)
		},
		primary: w.next(th, l, s),
	}
	if t.dirty && saved && !s.running {
		f.status = i18n.T(l, "wiz.byok.saveFirst")
	}
	return f
}
