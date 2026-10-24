package providers

import (
	"strings"

	"github.com/xlsft/companion/internal/llm"
)

// Preset is a known provider: what the setup form fills in when the user
// picks it. Nothing here is required; every field stays editable, and a
// preset is only a starting point.
type Preset struct {
	ID      string
	Name    string // shown as is: provider names are not translated
	Kind    llm.Kind
	BaseURL string
	// Model is a default that sees images, since Companion sends the
	// screen; "" when the server serves whatever the user loaded.
	Model string
	// Local presets run on this computer and need no API key.
	Local bool
}

// Presets lists the providers the setup form offers, hosted ones first.
var Presets = []Preset{
	{ID: "anthropic", Name: "Anthropic", Kind: llm.KindAnthropic, BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-5-5"},
	{ID: "openai", Name: "OpenAI", Kind: llm.KindOpenAI, BaseURL: "https://api.openai.com/v1", Model: "gpt-5"},
	{ID: "gemini", Name: "Google Gemini", Kind: llm.KindOpenAI, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Model: "gemini-2.5-flash"},
	{ID: "openrouter", Name: "OpenRouter", Kind: llm.KindOpenAI, BaseURL: "https://openrouter.ai/api/v1", Model: "openrouter/auto"},
	{ID: "mistral", Name: "Mistral", Kind: llm.KindOpenAI, BaseURL: "https://api.mistral.ai/v1", Model: "mistral-medium-latest"},
	{ID: "xai", Name: "xAI", Kind: llm.KindOpenAI, BaseURL: "https://api.x.ai/v1", Model: "grok-4"},
	{ID: "groq", Name: "Groq", Kind: llm.KindOpenAI, BaseURL: "https://api.groq.com/openai/v1", Model: "meta-llama/llama-4-scout-17b-16e-instruct"},
	{ID: "deepseek", Name: "DeepSeek", Kind: llm.KindOpenAI, BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
	{ID: "ollama", Name: "Ollama", Kind: llm.KindOpenAI, BaseURL: "http://localhost:11434/v1", Model: "gemma3", Local: true},
	{ID: "lmstudio", Name: "LM Studio", Kind: llm.KindOpenAI, BaseURL: "http://localhost:1234/v1", Local: true},
	{ID: "vllm", Name: "vLLM", Kind: llm.KindOpenAI, BaseURL: "http://localhost:8000/v1", Local: true},
}

// PresetFor is the preset whose endpoint baseURL is, so a saved provider
// shows the preset it came from; ok is false for a custom endpoint.
func PresetFor(kind llm.Kind, baseURL string) (Preset, bool) {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, p := range Presets {
		if p.Kind == kind && strings.EqualFold(p.BaseURL, u) {
			return p, true
		}
	}
	return Preset{}, false
}
