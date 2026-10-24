package ui

import (
	"golang.org/x/image/font/sfnt"

	"github.com/xlsft/companion/internal/assets/fonts"
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

// Every character of a CJK catalogue (and the language names shown in every
// picker) must have a glyph in Google Sans or in that language's bundled
// Noto Sans CJK subset, or the UI would show tofu. build.py cuts the
// subsets from the catalogues, so a new string needs `build.py --cjk`.
func TestCJKCataloguesHaveGlyphs(t *testing.T) {
	var buf sfnt.Buffer
	parse := func(name string) *sfnt.Font {
		f, err := sfnt.Parse(fonts.Must(name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return f
	}
	has := func(f *sfnt.Font, r rune) bool {
		i, err := f.GlyphIndex(&buf, r)
		return err == nil && i != 0
	}
	sans := parse(fonts.SansRegular)
	for code, files := range fonts.CJK {
		cjk := []*sfnt.Font{parse(files[0]), parse(files[1])}
		var texts []string
		for k := range i18n.Keys() {
			texts = append(texts, i18n.T(i18n.Lang(code), k))
		}
		for _, l := range i18n.Languages {
			texts = append(texts, l.Native)
		}
		seen := map[rune]bool{}
		for _, s := range texts {
			for _, r := range s {
				if seen[r] || r == ' ' || r == '\n' {
					continue
				}
				seen[r] = true
				// Latin and Cyrillic are Google Sans's; only the rest needs
				// the CJK face, in both weights.
				if has(sans, r) {
					continue
				}
				for i, f := range cjk {
					if !has(f, r) {
						t.Errorf("%s: no glyph for %q (%U) in %s", code, r, r, files[i])
					}
				}
			}
		}
	}
}
