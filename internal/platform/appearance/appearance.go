// Package appearance reads the system light/dark preference and follows it
// live. Platform files supply how to read it; this file holds the shared
// state, the polling helper and the fixed override used by spikes.
package appearance

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// state is the current scheme plus a coalescing change signal.
type state struct {
	mu   sync.Mutex
	dark bool
	ch   chan struct{}
}

func newState() *state { return &state{ch: make(chan struct{}, 1)} }

func (s *state) Dark() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dark
}

func (s *state) Changes() <-chan struct{} { return s.ch }

// set stores v and signals when it differs.
func (s *state) set(v bool) {
	s.mu.Lock()
	changed := s.dark != v
	s.dark = v
	s.mu.Unlock()
	if changed {
		select {
		case s.ch <- struct{}{}:
		default: // one pending signal is enough
		}
	}
}

// pollEvery is how often sources without a change signal are re-read.
const pollEvery = 2 * time.Second

// poll re-reads the scheme until ctx ends; a failed read keeps the last
// known value.
func (s *state) poll(ctx context.Context, every time.Duration, read func(context.Context) (bool, error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if v, err := read(ctx); err == nil {
				s.set(v)
			}
		}
	}
}

// decide folds the Linux sources into one answer. portal is the XDG
// color-scheme value (1 dark, 2 light, 0 no preference) and havePortal says
// whether it could be read; gsettings is org.gnome.desktop.interface
// color-scheme and gtkTheme its gtk-theme, either possibly empty.
func decide(portal uint32, havePortal bool, gsettings, gtkTheme string) bool {
	if havePortal {
		switch portal {
		case 1:
			return true
		case 2:
			return false
		}
	}
	switch strings.Trim(strings.TrimSpace(gsettings), "'\"") {
	case "prefer-dark":
		return true
	case "prefer-light":
		return false
	}
	return strings.Contains(strings.ToLower(gtkTheme), "dark")
}

// Fixed is a source that never changes; spikes use it to force a scheme.
func Fixed(dark bool) contracts.AppearanceSource {
	s := newState()
	s.dark = dark
	return fixed{s}
}

type fixed struct{ *state }

func (fixed) Start(context.Context) error { return nil }

// Choose maps a -theme flag value to a source: "auto" follows the system,
// "dark" and "light" force a scheme (screenshots).
func Choose(mode string) (contracts.AppearanceSource, error) {
	switch mode {
	case "auto", "":
		return New(), nil
	case "dark":
		return Fixed(true), nil
	case "light":
		return Fixed(false), nil
	}
	return nil, fmt.Errorf("unknown theme %q (auto, dark or light)", mode)
}
