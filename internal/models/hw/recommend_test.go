package hw

import (
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/models"
)

func TestRecommendByLanguage(t *testing.T) {
	cat := mustCatalog(t)
	slow := profile(32, false)
	tests := []struct {
		name string
		p    Profile
		lang string
		want string // model id, or "whisper" for any whisper fallback
	}{
		{"english", profile(32, true), "en", "parakeet-tdt-0.6b-v3-int8"},
		{"english gpu", profile(16, true, BackendCUDA), "en", "parakeet-tdt-0.6b-v3-int8"},
		{"german", profile(16, true), "de", "parakeet-tdt-0.6b-v3-int8"},
		{"russian", profile(16, true), "ru", "parakeet-tdt-0.6b-v3-int8"},
		{"ukrainian", profile(8, true), "uk", "parakeet-tdt-0.6b-v3-int8"},
		{"auto", profile(16, true), "auto", "parakeet-tdt-0.6b-v3-int8"},
		{"chinese", profile(16, true), "zh", "sense-voice-small-int8"},
		{"japanese", profile(16, true), "ja", "sense-voice-small-int8"},
		{"korean", profile(16, true), "ko", "sense-voice-small-int8"},
		{"cantonese", profile(16, true), "yue", "sense-voice-small-int8"},
		{"turkish falls back", profile(16, true), "tr", "whisper"},
		{"arabic falls back", profile(16, true), "ar", "whisper"},
		{"english low ram", profile(1, true), "en", "zipformer-streaming-en-int8"},
		{"german low ram falls back", profile(1, true), "de", "whisper"},
		{"english no avx2", slow, "en", "zipformer-streaming-en-int8"},
		{"chinese no avx2", slow, "zh", "zipformer-streaming-zh-en-int8"},
		{"german no avx2", slow, "de", "parakeet-tdt-0.6b-v3-int8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, reason := Recommend(tt.p, cat, tt.lang)
			if reason == "" {
				t.Fatal("empty reason")
			}
			if tt.want == "whisper" {
				if m.Family != models.FamilyWhisper || !strings.HasPrefix(reason, "fallback") {
					t.Fatalf("got %s (%s), want a whisper fallback", m.ID, reason)
				}
				if tt.lang != "en" && !m.Multilingual() {
					t.Fatalf("fallback %s is English-only", m.ID)
				}
				return
			}
			if m.ID != tt.want {
				t.Fatalf("got %s (%s), want %s", m.ID, reason, tt.want)
			}
		})
	}
}

func TestSpeechLanguage(t *testing.T) {
	tests := []struct{ voice, ui, want string }{
		{"de", "ru", "de"},
		{"auto", "ru", "ru"},
		{"", "ja", "ja"},
		{"auto", "", "auto"},
	}
	for _, tt := range tests {
		if got := SpeechLanguage(tt.voice, tt.ui); got != tt.want {
			t.Errorf("SpeechLanguage(%q, %q) = %q, want %q", tt.voice, tt.ui, got, tt.want)
		}
	}
}
