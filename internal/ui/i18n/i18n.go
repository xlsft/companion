// Package i18n is the ru/en text catalogue shared by the shell and its
// sub-packages (wizard, kit). Each package registers its own keys at init,
// so a sub-package can own its strings without importing internal/ui.
package i18n

import (
	"fmt"
	"sync"
)

// Lang is a UI language; values match settings.UI.Language.
type Lang string

const (
	RU Lang = "ru"
	EN Lang = "en"
)

var (
	mu      sync.RWMutex
	catalog = map[string][2]string{}
)

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

// T returns the text for key in l, falling back to English and then to the
// key itself so a missing entry is visible but harmless.
func T(l Lang, key string) string {
	mu.RLock()
	s, ok := catalog[key]
	mu.RUnlock()
	if !ok {
		return key
	}
	if l == RU && s[1] != "" {
		return s[1]
	}
	return s[0]
}

// Tf is T with fmt.Sprintf arguments.
func Tf(l Lang, key string, args ...any) string { return fmt.Sprintf(T(l, key), args...) }

// Of maps a settings language code to a Lang; Russian is the default.
func Of(code string) Lang {
	if code == string(EN) {
		return EN
	}
	return RU
}
