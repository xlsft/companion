//go:build linux || windows || (darwin && cgo)

package tray

import (
	"context"
	"errors"
	"sync"

	"fyne.io/systray"

	"github.com/xlsft/companion/pkg/contracts"
)

var errNotRunning = errors.New("tray: not running")

type sysTray struct {
	mu       sync.Mutex
	started  bool
	running  bool
	stop     func()
	onSelect func(id string)
}

func newTray() contracts.Tray { return &sysTray{} }

func (t *sysTray) Start(ctx context.Context, tooltip string, items []contracts.TrayMenuItem, onSelect func(string)) error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return errors.New("tray: already started")
	}
	t.started = true
	t.onSelect = onSelect
	t.mu.Unlock()

	if err := probe(); err != nil {
		return err
	}
	icon := platformIcon()
	appearance := func() {
		systray.SetIcon(icon)
		systray.SetTooltip(tooltip)
	}
	presetAppearance(appearance)
	ready := make(chan struct{})
	stop := run(func() {
		appearance()
		close(ready)
	})
	t.mu.Lock()
	t.stop = stop
	t.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		stop()
		return ctx.Err()
	}
	t.mu.Lock()
	t.running = true
	t.mu.Unlock()
	return t.SetItems(items)
}

func (t *sysTray) SetItems(items []contracts.TrayMenuItem) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return errNotRunning
	}
	// ResetMenu closes the click channels of the old items, which ends
	// their listeners.
	systray.ResetMenu()
	for _, it := range items {
		mi := systray.AddMenuItem(it.Label, "")
		if it.Disabled {
			mi.Disable()
		}
		go t.listen(mi.ClickedCh, it.ID)
	}
	return nil
}

func (t *sysTray) listen(clicked <-chan struct{}, id string) {
	for range clicked {
		t.mu.Lock()
		fn := t.onSelect
		t.mu.Unlock()
		if fn != nil {
			fn(id)
		}
	}
}

func (t *sysTray) Stop() error {
	t.mu.Lock()
	stop := t.stop
	t.stop = nil
	t.running = false
	t.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}
