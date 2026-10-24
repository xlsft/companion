package wizard

import (
	"strings"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
)

func init() {
	i18n.Register(map[string][2]string{
		"stt.tag.offline":   {"Offline", "Офлайн"},
		"stt.tag.streaming": {"Streaming", "Потоковая"},
		"stt.tag.rec":       {"Recommended", "Рекомендуем"},
		"stt.tag.fallback":  {"Fallback", "Запасная"},
		"stt.langs":         {"%d languages", "Языков: %d"},
		"stt.unpacking":     {"Unpacking · %s of %s", "Распаковка · %s из %s"},
		"stt.inUse":         {"In use", "Используется"},
		"stt.model":         {"Speech model", "Модель распознавания"},
		"stt.model.d":       {"Runs on this computer. It is downloaded once and then works offline.", "Работает на этом компьютере. Скачивается один раз, дальше работает без интернета."},

		"stt.desc.parakeet-tdt-0.6b-v3-int8":      {"Best accuracy for 25 European languages, with punctuation and capitals", "Лучшая точность для 25 европейских языков, с пунктуацией и заглавными"},
		"stt.desc.parakeet-tdt-0.6b-v2-int8":      {"English only, with punctuation and capitals", "Только английский, с пунктуацией и заглавными"},
		"stt.desc.sense-voice-small-int8":         {"Chinese, Cantonese, English, Japanese and Korean, with punctuation", "Китайский, кантонский, английский, японский и корейский, с пунктуацией"},
		"stt.desc.zipformer-streaming-en-int8":    {"English, text appears while you speak; no punctuation", "Английский, текст появляется во время речи; без пунктуации"},
		"stt.desc.zipformer-streaming-zh-en-int8": {"Chinese and English, text appears while you speak", "Китайский и английский, текст появляется во время речи"},
		"stt.desc.whisper":                        {"Whisper, for languages the other models do not cover", "Whisper — для языков, которых нет у других моделей"},
		"stt.desc.whisper.en":                     {"Whisper, English only", "Whisper, только английский"},
	})
}

// langsOf names a model's languages briefly: "multilingual", a few codes,
// or a count.
func langsOf(l i18n.Lang, m models.Manifest) string {
	switch {
	case m.Multilingual():
		return i18n.T(l, "wiz.stt.multilingual")
	case len(m.Languages) > 5:
		return i18n.Tf(l, "stt.langs", len(m.Languages))
	}
	return strings.ToUpper(strings.Join(m.Languages, ", "))
}

// modelDesc is the one-line description of a catalogue model.
func modelDesc(l i18n.Lang, m models.Manifest) string {
	if k := "stt.desc." + m.ID; i18n.Has(k) {
		return i18n.T(l, k)
	}
	if m.Family == models.FamilyWhisper && !m.Multilingual() {
		return i18n.T(l, "stt.desc.whisper.en")
	}
	if m.Family == models.FamilyWhisper {
		return i18n.T(l, "stt.desc.whisper")
	}
	return langsOf(l, m)
}

// ModelOption is a model's row in a picker: name, kind and status tags,
// download size and description.
func ModelOption(l i18n.Lang, m models.Manifest, recommended, installed bool) kit.RichOption {
	kind := "stt.tag.offline"
	switch {
	case m.Fallback:
		kind = "stt.tag.fallback"
	case m.Streaming():
		kind = "stt.tag.streaming"
	}
	o := kit.RichOption{
		Title: m.Name,
		Tags:  []kit.RichTag{{Text: i18n.T(l, kind), Kind: kit.TagNeutral}},
		Meta:  size(l, m.Size),
		Desc:  modelDesc(l, m),
	}
	if recommended {
		o.Tags = append(o.Tags, kit.RichTag{Text: i18n.T(l, "stt.tag.rec"), Kind: kit.TagAccent})
	}
	if installed {
		o.Tags = append(o.Tags, kit.RichTag{Text: i18n.T(l, "wiz.stt.ready"), Kind: kit.TagNeutral})
	}
	return o
}

// ModelOptions are the rows for cat; sel is the index of selected or -1.
func ModelOptions(l i18n.Lang, cat models.Catalog, rec, selected string, installed map[string]bool) (opts []kit.RichOption, sel int) {
	sel = -1
	opts = make([]kit.RichOption, len(cat))
	for i, m := range cat {
		opts[i] = ModelOption(l, m, m.ID == rec, installed[m.ID])
		if m.ID == selected {
			sel = i
		}
	}
	return opts, sel
}
