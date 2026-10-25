package wizard

import (
	"strings"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// customTile is the tile of a server of the user's own; it is the only
// place that shows the address and the API it speaks.
const customTile = "custom"

// agentLogos are the services with a logo; the rest show an initial.
var agentLogos = map[string]icon.Name{
	"anthropic": icon.BrandAnthropic, "openai": icon.BrandOpenAI, "gemini": icon.BrandGemini,
	"openrouter": icon.BrandOpenRouter, "mistral": icon.BrandMistral, "ollama": icon.BrandOllama,
}

// agentMarks are the initials of services without a logo.
var agentMarks = map[string]string{"xai": "X", "groq": "Q", "deepseek": "D", "lmstudio": "LM", "vllm": "vL", customTile: "+"}

// agentTiles are the provider tiles: every preset, then the custom server.
func agentTiles(l i18n.Lang) ([]string, []kit.Tile) {
	keys := make([]string, 0, len(providers.Presets)+1)
	tiles := make([]kit.Tile, 0, len(providers.Presets)+1)
	for _, p := range providers.Presets {
		t := kit.Tile{Logo: agentLogos[p.ID], Mark: agentMarks[p.ID], Title: p.Name}
		switch {
		case p.Local:
			t.Sub = i18n.T(l, "wiz.tile.local")
		case i18n.Has("wiz.tile." + p.ID):
			t.Sub = i18n.T(l, "wiz.tile."+p.ID)
		}
		keys, tiles = append(keys, p.ID), append(tiles, t)
	}
	keys = append(keys, customTile)
	tiles = append(tiles, kit.Tile{Mark: agentMarks[customTile], Title: i18n.T(l, "wiz.tile.custom"), Sub: i18n.T(l, "wiz.tile.custom.d")})
	return keys, tiles
}

func (b *byokState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	w.mu.Lock()
	info, saving, saveErr, loaded := b.info, b.saving, b.saveErr, b.loaded
	w.mu.Unlock()
	enabled := w.cfg.Backends.Provider != nil && !saving
	keys, tiles := agentTiles(l)
	card := []layout.Widget{}
	custom := b.tile.Value == customTile
	name := ""
	if p, ok := providers.PresetFor(llm.Kind(b.kind.Value), b.baseURL.Text()); ok && !custom {
		name = p.Name
	}
	if custom {
		card = append(card,
			field(w, th, i18n.T(l, "wiz.byok.baseURL"), "http://192.168.1.20:8000/v1", &b.baseURL, enabled),
			kit.Labeled(th, w.label(i18n.T(l, "wiz.agent.speaks")), func(gtx layout.Context) layout.Dimensions {
				if !enabled {
					gtx = gtx.Disabled()
				}
				w.frame.actions = append(w.frame.actions, ActChoose)
				return kit.Segmented(gtx, th, &b.kind, []kit.Option{
					{Key: string(llm.KindOpenAI), Label: "OpenAI"},
					{Key: string(llm.KindAnthropic), Label: "Anthropic"},
				})
			}),
		)
	}
	keyLabel := i18n.T(l, "wiz.byok.key")
	if name != "" {
		keyLabel = i18n.Tf(l, "wiz.agent.keyFor", name)
	}
	keyHint := i18n.T(l, "wiz.byok.key.hint")
	if !providers.NeedsKey(llm.Kind(b.kind.Value), b.baseURL.Text()) {
		keyHint = i18n.T(l, "wiz.agent.keyOptional")
	}
	keyField := field(w, th, keyLabel, keyHint, &b.key, enabled)
	if info != nil && info.HasKey && !b.replacing {
		keyField = storedKey(w, th, l, keyLabel, enabled)
	}
	modelField := b.models.field(w, th, l, i18n.T(l, "wiz.byok.model.hint"), &b.model, enabled)
	if custom {
		card = append(card, kit.Flow(th, 200, 12, keyField, modelField))
		hdrB := kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.byok.headers"), Icon: icon.Add}
		if b.showHeaders {
			hdrB.Icon = icon.Up
			card = append(card, field(w, th, i18n.T(l, "wiz.byok.headers.hint"), "X-Header: value", &b.hdr, enabled))
		}
		card = append(card, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActHeaders, hdrB, enabled)
		})
	} else if b.tile.Value != "" {
		card = append(card, keyField, modelField)
	}
	rows := []layout.Widget{w.tileGrid(th, &b.tile, &b.tileList, keys, tiles, 4, enabled)}
	if len(card) > 0 {
		card = append(card, b.status(w, th, l, s, info, saving, saveErr, loaded))
		rows = append(rows, panel(th, kit.VStack(th, 14, card...)))
	}
	return stepBody{rows: rows}
}

// status is the one line under the form: what is happening or what to do,
// in plain words.
func (b *byokState) status(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot, info *providers.Info, saving bool, saveErr string, loaded bool) layout.Widget {
	r, ok := s.result(readiness.StepBYOK)
	switch {
	case w.cfg.Backends.Provider == nil:
		return statusText(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"))
	case !loaded:
		return statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.loading"))
	case saveErr != "":
		return statusText(w, th, theme.StatusError, fixText(l, saveErr))
	case saving:
		return statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.agent.saving"))
	case s.running:
		return statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.agent.checking"))
	case b.dirty || info == nil:
		return statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.agent.fill"))
	case ok && r.Status == readiness.Passed:
		var caps []string
		if r.Detail[providers.DetailCapPrefix+"vision"] == string(providers.StatusOK) {
			caps = append(caps, i18n.T(l, "wiz.agent.seesScreen"))
		}
		if r.Detail[providers.DetailCapPrefix+"tools"] == string(providers.StatusOK) {
			caps = append(caps, i18n.T(l, "wiz.agent.points"))
		}
		txt := i18n.T(l, "wiz.agent.connected")
		if len(caps) > 0 {
			txt += " · " + strings.Join(caps, " · ")
		}
		return statusText(w, th, theme.StatusOK, txt)
	case ok:
		return statusText(w, th, theme.StatusError, fixText(l, r.Code))
	}
	return statusText(w, th, theme.StatusUnknown, i18n.T(l, "wiz.agent.checking"))
}
