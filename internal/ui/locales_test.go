package ui

import (
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/ui/i18n"
	_ "github.com/xlsft/companion/internal/ui/wizard" // registers its strings
)

// Every language must translate every string the UI registers; a missing
// one would silently show English in the middle of another language.
func TestLocalesComplete(t *testing.T) {
	keys := i18n.Keys()
	for _, l := range i18n.Languages {
		if l.Code == i18n.EN || l.Code == i18n.RU {
			continue
		}
		var missing []string
		for k := range keys {
			if !i18n.Translated(l.Code, k) {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s: %d of %d strings untranslated, e.g. %s", l.Code, len(missing), len(keys), missing[0])
		}
	}
}

// Locale files may only translate registered keys, with the English
// text's format verbs in the same order.
func TestLocaleEntriesMatchCatalogue(t *testing.T) {
	keys := i18n.Keys()
	for _, l := range i18n.Languages {
		for _, k := range i18n.LocaleKeys(l.Code) {
			en, ok := keys[k]
			if !ok {
				t.Errorf("%s: unknown key %s", l.Code, k)
				continue
			}
			if got, want := verbs(i18n.T(l.Code, k)), verbs(en); got != want {
				t.Errorf("%s %s: verbs %q, English has %q", l.Code, k, got, want)
			}
		}
	}
}

// verbs uses verbRe from env_test.go.
func verbs(s string) string { return strings.Join(verbRe.FindAllString(s, -1), " ") }
