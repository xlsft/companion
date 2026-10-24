package sherpa

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xlsft/companion/internal/stt/engine"
)

// Model types in engine.Descriptor.Type.
const (
	TypeNemoTransducer      = "nemo_transducer"
	TypeSenseVoice          = "sense_voice"
	TypeZipformerTransducer = "zipformer_transducer"
)

// senseVoiceLangs are the languages SenseVoice accepts as a hint; anything
// else lets it detect.
var senseVoiceLangs = []string{"zh", "en", "ja", "ko", "yue"}

// senseVoiceHint maps a protocol language onto SenseVoice's option.
func senseVoiceHint(lang string) string {
	if slices.Contains(senseVoiceLangs, lang) {
		return lang
	}
	return "auto"
}

// senseVoiceLang turns SenseVoice's "<|en|>" tag into an ISO code; ""
// when it reported nothing usable.
func senseVoiceLang(tag string) string {
	l := strings.TrimSuffix(strings.TrimPrefix(tag, "<|"), "|>")
	if slices.Contains(senseVoiceLangs, l) {
		return l
	}
	return ""
}

// normalize tidies a hypothesis for display. Zipformer models trained on
// LibriSpeech-style transcripts emit upper case without punctuation;
// sentence case reads far better in the overlay and the chat, while text
// that already has mixed case (Parakeet, SenseVoice) is left alone.
func normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	hasUpper, hasLower := false, false
	for _, r := range s {
		hasUpper = hasUpper || unicode.IsUpper(r)
		hasLower = hasLower || unicode.IsLower(r)
	}
	if !hasUpper || hasLower {
		return s
	}
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if w == "i" || strings.HasPrefix(w, "i'") {
			words[i] = "I" + w[1:]
		}
	}
	s = strings.Join(words, " ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// Detect builds a descriptor for a model directory unpacked by hand from a
// sherpa-onnx release archive (development use; installed models carry
// model.json). int8 files win over full precision, as the catalogue
// installs them.
func Detect(dir string) (engine.Descriptor, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return engine.Descriptor{}, false
	}
	pick := func(prefix string) string {
		var plain, int8 string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".onnx") {
				continue
			}
			if strings.HasSuffix(n, ".int8.onnx") {
				int8 = n
			} else if plain == "" {
				plain = n
			}
		}
		if int8 != "" {
			return int8
		}
		return plain
	}
	if _, err := os.Stat(filepath.Join(dir, "tokens.txt")); err != nil {
		return engine.Descriptor{}, false
	}
	name := strings.ToLower(filepath.Base(dir))
	d := engine.Descriptor{Engine: "sherpa", Kind: engine.KindOffline, Files: map[string]string{engine.RoleTokens: "tokens.txt"}}
	enc, dec, join := pick("encoder"), pick("decoder"), pick("joiner")
	switch {
	case enc != "" && dec != "" && join != "":
		d.Files[engine.RoleEncoder], d.Files[engine.RoleDecoder], d.Files[engine.RoleJoiner] = enc, dec, join
		if strings.Contains(name, "streaming") {
			d.Type, d.Kind = TypeZipformerTransducer, engine.KindStreaming
		} else {
			d.Type = TypeNemoTransducer
		}
	case strings.Contains(name, "sense-voice") && pick("model") != "":
		d.Type = TypeSenseVoice
		d.Files[engine.RoleModel] = pick("model")
		d.Languages = slices.Clone(senseVoiceLangs)
	default:
		return engine.Descriptor{}, false
	}
	return d, d.Validate() == nil
}
