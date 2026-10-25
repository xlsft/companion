package tts

import (
	"net/url"
	"testing"
)

func TestTTSPresetsAreUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets {
		if p.ID == "" || p.Name == "" || seen[p.ID] {
			t.Errorf("%+v: empty or duplicate id/name", p)
		}
		seen[p.ID] = true
		if p.Provider != "openai" && p.Provider != "elevenlabs" && p.Provider != EdgeProviderID {
			t.Errorf("%s: provider %q", p.ID, p.Provider)
		}
		if p.BaseURL == "" {
			if p.Local {
				t.Errorf("%s: a local preset needs its address", p.ID)
			}
			continue
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" {
			t.Errorf("%s: base URL %q", p.ID, p.BaseURL)
			continue
		}
		if local := u.Hostname() == "localhost"; local != p.Local {
			t.Errorf("%s: local = %v but host is %s", p.ID, p.Local, u.Host)
		}
		if p.Local && len(p.Voices) == 0 {
			t.Errorf("%s: a local server preset needs its voices", p.ID)
		}
	}
}

func TestTTSPresetFor(t *testing.T) {
	tests := []struct {
		name, provider, url, want string
	}{
		{"openai default", "openai", "", "openai"},
		{"elevenlabs default", "elevenlabs", "", "elevenlabs"},
		{"edge", EdgeProviderID, "", EdgeProviderID},
		{"kokoro", "openai", "http://localhost:8880/v1/", "kokoro"},
		{"provider must match", "elevenlabs", "http://localhost:8880/v1", ""},
		{"custom", "openai", "http://tts.example.com/v1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := PresetFor(tt.provider, tt.url)
			if got := map[bool]string{true: p.ID}[ok]; got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNeedsKey(t *testing.T) {
	tests := []struct {
		provider, url string
		want          bool
	}{
		{"openai", "", true},
		{"openai", "https://openrouter.ai/api/v1", true},
		{"elevenlabs", "", true},
		{EdgeProviderID, "", false},
		{"openai", "http://localhost:8880/v1", false},
		{"openai", "http://tts.lan:9000/v1", false},
	}
	for _, tt := range tests {
		if got := NeedsKey(tt.provider, tt.url); got != tt.want {
			t.Errorf("NeedsKey(%q, %q) = %v, want %v", tt.provider, tt.url, got, tt.want)
		}
	}
}
