package tts

import "strings"

// Preset is a known speech service: what the setup form fills in when the
// user picks it. Every field stays editable; a preset is a starting point.
type Preset struct {
	ID   string
	Name string // shown as is: service names are not translated
	// Provider is the API the service speaks: "openai" (POST /audio/speech)
	// or "elevenlabs".
	Provider string
	// BaseURL and Model are "" for the provider's own defaults.
	BaseURL, Model string
	// Voices replaces the provider's static list for servers that ship
	// their own voices; nil keeps it.
	Voices []Voice
	// Local presets run on this computer and need no API key.
	Local bool
}

// Kokoro-82M's voices (v1.0), as Kokoro-FastAPI and Speaches serve them;
// the prefix is language and gender (a: American, b: British, e: Spanish,
// f: French, i: Italian, p: Portuguese, j: Japanese, z: Chinese).
var kokoroVoices = voiceList("af_heart", "af_bella", "af_nicole", "af_sarah", "af_sky", "am_adam", "am_michael",
	"bf_emma", "bf_isabella", "bm_george", "bm_lewis", "ef_dora", "ff_siwis", "if_sara", "pf_dora", "jf_alpha", "zf_xiaobei")

func voiceList(ids ...string) []Voice {
	out := make([]Voice, len(ids))
	for i, id := range ids {
		out[i] = Voice{ID: id, Name: id}
	}
	return out
}

// Presets lists the speech services the setup form offers, hosted first.
var Presets = []Preset{
	{ID: "openai", Name: "OpenAI", Provider: "openai"},
	{ID: "elevenlabs", Name: "ElevenLabs", Provider: "elevenlabs"},
	{ID: "kokoro", Name: "Kokoro", Provider: "openai", BaseURL: "http://localhost:8880/v1", Model: "kokoro", Voices: kokoroVoices, Local: true},
	{ID: "speaches", Name: "Speaches", Provider: "openai", BaseURL: "http://localhost:8000/v1", Model: "speaches-ai/Kokoro-82M-v1.0-ONNX", Voices: kokoroVoices, Local: true},
}

// PresetFor is the preset a saved provider and endpoint came from; ok is
// false for a custom endpoint. An empty baseURL is the provider's default.
func PresetFor(provider, baseURL string) (Preset, bool) {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, p := range Presets {
		if p.Provider == provider && strings.EqualFold(p.BaseURL, u) {
			return p, true
		}
	}
	return Preset{}, false
}
