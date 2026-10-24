package readiness

import "testing"

func TestPhraseMatches(t *testing.T) {
	tests := []struct {
		name, want, got string
		ok              bool
	}{
		{"exact ru", MicPhrase("ru"), "Привет, это проверка микрофона.", true},
		{"case and punctuation", MicPhrase("en"), "hello this is a MICROPHONE test!", true},
		{"one word off", MicPhrase("en"), "Hello, this is the microphone test", true},
		{"ё", "ещё", "еще", true},
		{"unrelated", MicPhrase("en"), "thanks for watching", false},
		{"empty", MicPhrase("ru"), "", false},
		{"empty want", "", "anything", false},
	}
	for _, tt := range tests {
		if got := PhraseMatches(tt.want, tt.got); got != tt.ok {
			t.Errorf("%s: PhraseMatches = %v", tt.name, got)
		}
	}
}

func TestMicPhrase(t *testing.T) {
	for lang, want := range map[string]string{
		"ru": "Привет, это проверка микрофона",
		"en": "Hello, this is a microphone test",
		"de": "Привет, это проверка микрофона",
	} {
		if got := MicPhrase(lang); got != want {
			t.Errorf("MicPhrase(%s) = %q", lang, got)
		}
	}
}
