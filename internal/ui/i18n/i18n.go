// Package i18n is the text catalogue shared by the shell and its
// sub-packages (wizard, kit). Each package registers its own keys at init
// with the English and Russian text, so a sub-package can own its strings
// without importing internal/ui. The other languages are embedded JSON
// catalogues (locales/<code>.json) keyed the same way; any missing entry
// falls back to English.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

// Lang is a UI language; values match settings.UI.Language.
type Lang string

const (
	EN Lang = "en"
	ES Lang = "es"
	PT Lang = "pt"
	FR Lang = "fr"
	DE Lang = "de"
	IT Lang = "it"
	PL Lang = "pl"
	TR Lang = "tr"
	UK Lang = "uk"
	RU Lang = "ru"
)

// Language is a selectable UI language.
type Language struct {
	Code Lang
	// Native is the language's own name, shown in pickers so a user can
	// find it whatever the current language is.
	Native string
}

// Languages are the UI languages in picker order, English (the default)
// first. They mirror settings.UILanguages.
var Languages = []Language{
	{EN, "English"}, {ES, "Español"}, {PT, "Português"}, {FR, "Français"}, {DE, "Deutsch"},
	{IT, "Italiano"}, {PL, "Polski"}, {TR, "Türkçe"}, {UK, "Українська"}, {RU, "Русский"},
}

var (
	mu      sync.RWMutex
	catalog = map[string][2]string{}
)

//go:embed locales/*.json
var localeFS embed.FS

var (
	localesOnce sync.Once
	locales     map[Lang]map[string]string
)

func loadLocales() {
	locales = map[Lang]map[string]string{}
	for _, l := range Languages {
		if l.Code == EN || l.Code == RU {
			continue
		}
		b, err := localeFS.ReadFile("locales/" + string(l.Code) + ".json")
		if err != nil {
			continue // not translated yet: English fallback
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic("i18n: bad locale " + string(l.Code) + ": " + err.Error()) // embedded; covered by tests
		}
		locales[l.Code] = m
	}
}

// Register adds {en, ru} entries. A key registered twice is a programming
// error between packages, so it panics at init rather than shadowing text.
func Register(entries map[string][2]string) {
	mu.Lock()
	defer mu.Unlock()
	for k, v := range entries {
		if _, dup := catalog[k]; dup {
			panic("i18n: duplicate key " + k)
		}
		catalog[k] = v
	}
}

// Has reports whether key is registered.
func Has(key string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := catalog[key]
	return ok
}

// Keys returns every registered key with its English text.
func Keys() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]string, len(catalog))
	for k, v := range catalog {
		out[k] = v[0]
	}
	return out
}

// T returns the text for key in l, falling back to English and then to the
// key itself so a missing entry is visible but harmless.
func T(l Lang, key string) string {
	mu.RLock()
	s, ok := catalog[key]
	mu.RUnlock()
	if !ok {
		return key
	}
	switch l {
	case EN:
	case RU:
		if s[1] != "" {
			return s[1]
		}
	default:
		localesOnce.Do(loadLocales)
		if t := locales[l][key]; t != "" {
			return t
		}
	}
	return s[0]
}

// Translated reports whether a JSON-catalogue language has its own text
// for key (English and Russian live with the code and always do).
func Translated(l Lang, key string) bool {
	if l == EN || l == RU {
		return true
	}
	localesOnce.Do(loadLocales)
	return locales[l][key] != ""
}

// LocaleKeys returns the keys a JSON-catalogue language translates.
func LocaleKeys(l Lang) []string {
	localesOnce.Do(loadLocales)
	keys := make([]string, 0, len(locales[l]))
	for k := range locales[l] {
		keys = append(keys, k)
	}
	return keys
}

// Tf is T with fmt.Sprintf arguments.
func Tf(l Lang, key string, args ...any) string { return fmt.Sprintf(T(l, key), args...) }

// Of maps a settings language code to a Lang; English is the default.
func Of(code string) Lang {
	for _, l := range Languages {
		if string(l.Code) == code {
			return l.Code
		}
	}
	return EN
}
