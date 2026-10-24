package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"

	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
)

type fakeMemory struct {
	mu      sync.Mutex
	facts   []memory.Fact
	err     error
	cleared int
}

func (f *fakeMemory) Facts(context.Context) ([]memory.Fact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]memory.Fact(nil), f.facts...), f.err
}

func (f *fakeMemory) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.facts {
		if x.ID == id {
			f.facts = append(f.facts[:i], f.facts[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeMemory) Clear(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.facts, f.cleared = nil, f.cleared+1
	return nil
}

func (f *fakeMemory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.facts)
}

func (f *fakeMemory) clears() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleared
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestMemorySettings(t *testing.T) {
	fm := &fakeMemory{facts: []memory.Fact{
		{ID: "a", Kind: memory.KindName, Text: "The user's name is Anna"},
		{ID: "b", Kind: memory.KindOther, Text: "Is writing a report", Volatile: true},
	}}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Memory = fm })
	m, _ := h.mainBuilt()
	m.showSettings(secMemory)
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	sp, ok := m.pages[RouteSettings].(*settingsPage)
	if !ok || sp.mem == nil {
		t.Fatal("no memory view in Settings")
	}
	eventually(t, "facts loaded", func() bool {
		sp.mem.mu.Lock()
		defer sp.mem.mu.Unlock()
		return sp.mem.loaded
	})
	frame(r, h.sh, th, m)

	// Deleting a fact reaches the backend and the list reloads.
	sp.mem.dels["a"].Click()
	frame(r, h.sh, th, m)
	eventually(t, "fact deleted", func() bool { return fm.count() == 1 })
	eventually(t, "list reloaded", func() bool {
		sp.mem.mu.Lock()
		defer sp.mem.mu.Unlock()
		return len(sp.mem.facts) == 1
	})

	// Forget everything needs a confirmation; cancelling keeps the facts.
	sp.mem.clear.Click()
	frame(r, h.sh, th, m)
	if !sp.mem.confirming {
		t.Fatal("no confirmation asked")
	}
	sp.mem.cancel.Click()
	frame(r, h.sh, th, m)
	if sp.mem.confirming || fm.clears() != 0 || fm.count() != 1 {
		t.Fatalf("cancel: confirming=%v cleared=%d facts=%d", sp.mem.confirming, fm.clears(), fm.count())
	}
	sp.mem.clear.Click()
	frame(r, h.sh, th, m)
	sp.mem.yes.Click()
	frame(r, h.sh, th, m)
	eventually(t, "memory cleared", func() bool { return fm.count() == 0 })
	if fm.clears() != 1 {
		t.Errorf("cleared %d times", fm.clears())
	}

	// The switch is the Memory.Enabled setting the pipeline reads.
	if err := h.sh.opts.Settings.Update(context.Background(), func(s *settings.Settings) { s.Memory.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	frame(r, h.sh, th, m)
	if sp.memory.Value {
		t.Error("switch still on after disabling")
	}
}

func TestMemoryLoadErrorShown(t *testing.T) {
	fm := &fakeMemory{err: errors.New("db")}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Memory = fm })
	m, _ := h.mainBuilt()
	m.showSettings(secMemory)
	th := theme.New(true, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	sp := m.pages[RouteSettings].(*settingsPage)
	eventually(t, "load attempted", func() bool {
		sp.mem.mu.Lock()
		defer sp.mem.mu.Unlock()
		return sp.mem.loaded && sp.mem.failed
	})
	frame(r, h.sh, th, m)
}
