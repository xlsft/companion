package i18n

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/settings"
)

func TestT(t *testing.T) {
	Register(map[string][2]string{
		"test.both":   {"Hello", "Привет"},
		"test.enOnly": {"Only", ""},
	})
	tests := []struct {
		lang Lang
		key  string
		want string
	}{
		{EN, "test.both", "Hello"},
		{RU, "test.both", "Привет"},
		{RU, "test.enOnly", "Only"},
		{RU, "test.missing", "test.missing"},
	}
	for _, tt := range tests {
		if got := T(tt.lang, tt.key); got != tt.want {
			t.Errorf("T(%s, %s) = %q, want %q", tt.lang, tt.key, got, tt.want)
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register(map[string][2]string{"test.dup": {"a", "б"}})
	defer func() {
		if recover() == nil {
			t.Error("duplicate key did not panic")
		}
	}()
	Register(map[string][2]string{"test.dup": {"a", "б"}})
}

func TestOf(t *testing.T) {
	for code, want := range map[string]Lang{"en": EN, "ru": RU, "": EN, "de": DE, "xx": EN} {
		if got := Of(code); got != want {
			t.Errorf("Of(%q) = %s", code, got)
		}
	}
}

func TestLanguagesMatchSettings(t *testing.T) {
	var codes []string
	for _, l := range Languages {
		codes = append(codes, string(l.Code))
	}
	if !slices.Equal(codes, settings.UILanguages) {
		t.Errorf("i18n %v != settings %v", codes, settings.UILanguages)
	}
}

// Locale files may only translate registered keys and must keep the
// English text's format verbs, or Tf would print %!(EXTRA…) garbage.
func TestLocaleEntriesAreValid(t *testing.T) {
	Register(map[string][2]string{"test.fmt": {"%d of %s", "%d из %s"}})
	localesOnce.Do(loadLocales)
	for l, m := range locales {
		for k, v := range m {
			en, ok := catalog[k]
			if !ok {
				if !strings.HasPrefix(k, "test.") {
					t.Errorf("%s: unknown key %s", l, k)
				}
				continue
			}
			if verbs(v) != verbs(en[0]) {
				t.Errorf("%s %s: verbs %q, English has %q", l, k, verbs(v), verbs(en[0]))
			}
		}
	}
}

func TestFallbackToEnglish(t *testing.T) {
	Register(map[string][2]string{"test.fallback": {"Fallback", "Запасной"}})
	if got := T(DE, "test.fallback"); got != "Fallback" {
		t.Errorf("untranslated key in de = %q", got)
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z%]`)

func verbs(s string) string { return strings.Join(verbRe.FindAllString(s, -1), " ") }
