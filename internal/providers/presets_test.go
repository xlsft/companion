package providers

import (
	"net/url"
	"testing"

	"github.com/xlsft/companion/internal/llm"
)

func TestPresetsAreUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets {
		if p.ID == "" || p.Name == "" || seen[p.ID] {
			t.Errorf("%+v: empty or duplicate id/name", p)
		}
		seen[p.ID] = true
		if p.Kind != llm.KindOpenAI && p.Kind != llm.KindAnthropic {
			t.Errorf("%s: kind %q", p.ID, p.Kind)
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" {
			t.Errorf("%s: base URL %q", p.ID, p.BaseURL)
			continue
		}
		if local := u.Hostname() == "localhost"; local != p.Local {
			t.Errorf("%s: local = %v but host is %s", p.ID, p.Local, u.Host)
		}
		if !p.Local && (u.Scheme != "https" || p.Model == "") {
			t.Errorf("%s: hosted presets need https and a default model", p.ID)
		}
	}
}

func TestPresetFor(t *testing.T) {
	tests := []struct {
		name string
		kind llm.Kind
		url  string
		want string
	}{
		{"exact", llm.KindOpenAI, "https://api.openai.com/v1", "openai"},
		{"trailing slash and spaces", llm.KindOpenAI, " https://api.openai.com/v1/ ", "openai"},
		{"case", llm.KindAnthropic, "https://API.anthropic.com", "anthropic"},
		{"local", llm.KindOpenAI, "http://localhost:11434/v1", "ollama"},
		{"kind must match", llm.KindAnthropic, "https://api.openai.com/v1", ""},
		{"custom", llm.KindOpenAI, "https://llm.example.com/v1", ""},
		{"empty", llm.KindOpenAI, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := PresetFor(tt.kind, tt.url)
			if got := map[bool]string{true: p.ID}[ok]; got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
