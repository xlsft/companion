package readiness

import (
	"strings"
	"unicode"
)

// Result.Detail conventions shared by the step checks and the wizard UI.
// Values are technical codes only: DetailOK, DetailSkipped, or a failure
// code that the wizard shows as the technical status with its fix text
// (FR-ONB-007).
const (
	DetailOK      = "ok"
	DetailSkipped = "skipped"
)

// Permission step: Detail keys are capability names (contracts.Capability
// values such as "microphone", "screen_capture", "global_hotkey",
// "overlay_click_through"); values are one of these or a failure code.
const (
	PermGranted         = "granted"
	PermFallback        = "fallback" // usable through the documented fallback
	PermNeedsPermission = "needs_permission"
	PermDenied          = "denied"
	PermUnavailable     = "unavailable"
	// PermNeedsBinding: the compositor has no global-shortcut portal; the
	// user binds a key to the app's command (instructions in the wizard).
	PermNeedsBinding = "needs_compositor_binding"
)

// PermissionOK reports whether a permission detail value lets setup pass.
func PermissionOK(v string) bool {
	return v == PermGranted || v == PermFallback || v == DetailOK
}

// Voice check (StepTTS, OC-133): Companion speaks MicPhrase through the
// configured voice while the microphone records, so one check covers the
// voice, the microphone and recognition.
const (
	// DetailSpoke is DetailOK once the phrase played to the end, else the
	// speech failure code (also the Result's Code; the step then fails).
	DetailSpoke = "spoke"
	// DetailHeard is DetailOK when the recording matched the phrase,
	// HeardNoSpeech or HeardMismatch otherwise, DetailSkipped when nothing
	// played, or a microphone or recogniser failure code. Only DetailSpoke
	// decides StepTTS: headphones keep a working setup from hearing itself.
	DetailHeard   = "heard"
	HeardNoSpeech = "no_speech"
	HeardMismatch = "mismatch"
)

// DetailSource on a passed StepSTT tells where the proof came from.
const (
	DetailSource   = "source"
	SourceLoopback = "loopback" // the voice check's recording matched
	SourceUser     = "user"     // the user read MicPhrase aloud
)

// End-to-end stages, in order (FR-ONB-009). Each is a Detail key whose
// value is DetailOK, DetailSkipped or a failure code.
var EndToEndStages = []string{"speech", "stt", "capture", "llm", "tts", "pointing"}

// micPhrases is the fixed sentence read aloud in the STT step.
var micPhrases = map[string]string{
	"ru": "Привет, это проверка микрофона",
	"en": "Hello, this is a microphone test",
}

// MicPhrase returns the fixed microphone test phrase for a UI language;
// Russian is the default like the UI.
func MicPhrase(lang string) string {
	if p, ok := micPhrases[lang]; ok {
		return p
	}
	return micPhrases["ru"]
}

// PhraseLanguage is the language MicPhrase(lang) is written in, for the
// recogniser to listen for.
func PhraseLanguage(lang string) string {
	if _, ok := micPhrases[lang]; ok {
		return lang
	}
	return "ru"
}

// ttsSentences is the fixed sentence the TTS check plays.
var ttsSentences = map[string]string{
	"ru": "Привет! Так звучит мой голос.",
	"en": "Hello! This is how my voice sounds.",
}

// TTSSentence returns the fixed sentence the TTS step plays for a UI
// language; Russian is the default.
func TTSSentence(lang string) string {
	if s, ok := ttsSentences[lang]; ok {
		return s
	}
	return ttsSentences["ru"]
}

// PhraseMatches compares a transcript with the expected phrase loosely:
// case, punctuation and ё/е are ignored and at least 60% of the expected
// words must appear, since small models misspell short words.
func PhraseMatches(want, got string) bool {
	ww, gw := words(want), words(got)
	if len(ww) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range gw {
		have[w] = true
	}
	hits := 0
	for _, w := range ww {
		if have[w] {
			hits++
		}
	}
	return hits*10 >= len(ww)*6
}

func words(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "ё", "е"), "Ё", "Е"))
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
