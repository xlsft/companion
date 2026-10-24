package ui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"

	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/update"
)

// fakeUpdates is an UpdateBackend whose state the test sets.
type fakeUpdates struct {
	mu                         sync.Mutex
	st                         update.State
	installs, checks, notes    int
	watchers, cancelledWatches int
}

func (f *fakeUpdates) State() update.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}
func (f *fakeUpdates) set(st update.State) { f.mu.Lock(); f.st = st; f.mu.Unlock() }
func (f *fakeUpdates) CheckNow()           { f.mu.Lock(); f.checks++; f.mu.Unlock() }
func (f *fakeUpdates) Install()            { f.mu.Lock(); f.installs++; f.mu.Unlock() }
func (f *fakeUpdates) OpenNotes()          { f.mu.Lock(); f.notes++; f.mu.Unlock() }
func (f *fakeUpdates) Watch(func()) func() {
	f.mu.Lock()
	f.watchers++
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.cancelledWatches++; f.mu.Unlock() }
}

func mustVersion(t *testing.T, s string) update.Version {
	v, err := update.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func availableState(t *testing.T) update.State {
	return update.State{
		Current:   mustVersion(t, "0.0.1"),
		Latest:    &update.Release{Tag: "v0.2.0", Version: mustVersion(t, "0.2.0"), URL: "https://example.invalid/notes"},
		LastCheck: time.Now(),
	}
}

func TestUpdateBannerStates(t *testing.T) {
	base := availableState(t)
	with := func(fn func(*update.State)) update.State {
		st := base
		fn(&st)
		return st
	}
	tests := []struct {
		name    string
		st      update.State
		show    bool
		title   string
		detail  string
		status  theme.Status
		hasProg bool
	}{
		{"nothing new", update.State{Current: base.Current}, false, "", "", 0, false},
		{"available", base, true, "Companion v0.2.0 is available", "", theme.StatusUnknown, false},
		{"package manager", with(func(s *update.State) { s.Managed = "package_managed" }), true, "Companion v0.2.0 is available", "package manager", theme.StatusUnknown, false},
		{"downloading", with(func(s *update.State) { s.Phase, s.Done, s.Total = update.PhaseDownloading, 1<<20, 4<<20 }), true, "Updating to v0.2.0…", "1.0 of 4.0 MB", theme.StatusUnknown, true},
		{"verifying", with(func(s *update.State) { s.Phase = update.PhaseVerifying }), true, "Updating to v0.2.0…", "signature", theme.StatusUnknown, false},
		{"installing", with(func(s *update.State) { s.Phase = update.PhaseInstalling }), true, "Updating to v0.2.0…", "Installing", theme.StatusUnknown, false},
		{"restart needed", with(func(s *update.State) { s.Phase = update.PhaseRestartNeeded }), true, "Companion v0.2.0 is installed", "open Companion again", theme.StatusOK, false},
		{"bad signature", with(func(s *update.State) { s.Phase, s.Err = update.PhaseFailed, "bad_signature" }), true, "Couldn't update to v0.2.0", "signature is not valid", theme.StatusError, false},
		{"unknown failure", with(func(s *update.State) { s.Phase, s.Err = update.PhaseFailed, "weird" }), true, "Couldn't update to v0.2.0", "Something went wrong", theme.StatusError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, ok := bannerFor(LangEN, tt.st)
			if ok != tt.show {
				t.Fatalf("shown = %v", ok)
			}
			if !ok {
				return
			}
			if b.Title != tt.title || !strings.Contains(b.Detail, tt.detail) || b.Status != tt.status || (b.Progress >= 0) != tt.hasProg {
				t.Errorf("banner = %+v", b)
			}
			// Every state has Russian text too.
			if ru, _ := bannerFor(LangRU, tt.st); ru.Title == b.Title && tt.title != "" {
				t.Errorf("ru title untranslated: %q", ru.Title)
			}
		})
	}
}

// The same banner sits on top of the main window and the setup window,
// and its buttons reach the updater.
func TestUpdateBannerInBothWindows(t *testing.T) {
	fake := &fakeUpdates{st: availableState(t)}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Updates = fake })
	m, _ := h.mainBuilt()
	wiz := newWizardUI(h.sh)
	th := theme.New(false, 1)
	for _, w := range []struct {
		name    string
		content surface
		b       *updateBanner
	}{{"main", m, m.update}, {"setup", wiz, wiz.update}} {
		r := new(input.Router)
		frame(r, h.sh, th, w.content)
		before := fake.installs
		w.b.install.Click()
		w.b.notes.Click()
		frame(r, h.sh, th, w.content)
		if fake.installs != before+1 {
			t.Errorf("%s: Update did not install", w.name)
		}
		if fake.notes == 0 {
			t.Errorf("%s: Release notes not opened", w.name)
		}
	}
	r := new(input.Router)
	// A package-managed install offers no Update button.
	fake.set(func() update.State { st := availableState(t); st.Managed = "package_managed"; return st }())
	before := fake.installs
	frame(r, h.sh, th, m)
	m.update.install.Click()
	frame(r, h.sh, th, m)
	if fake.installs != before {
		t.Error("Update offered for a package-managed install")
	}
	if fake.watchers != 1 {
		t.Errorf("shell watches = %d", fake.watchers)
	}
}

func TestUpdateSettingsRows(t *testing.T) {
	fake := &fakeUpdates{st: availableState(t)}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Updates = fake })
	m, _ := h.mainBuilt()
	th := theme.New(false, 1)
	r := new(input.Router)
	m.showSettings(secGeneral)
	frame(r, h.sh, th, m)
	sp := m.pages[RouteSettings].(*settingsPage)
	sp.checkNow.Click()
	frame(r, h.sh, th, m)
	if fake.checks != 1 {
		t.Errorf("Check now: checks = %d", fake.checks)
	}
	if sp.autoUpd.Value != true || sp.preUpd.Value != false {
		t.Errorf("toggles = %v %v", sp.autoUpd.Value, sp.preUpd.Value)
	}
}

func TestUpdateStatusLine(t *testing.T) {
	now := time.Date(2026, 10, 24, 15, 0, 0, 0, time.Local)
	at := time.Date(2026, 10, 24, 14, 2, 0, 0, time.Local)
	v := mustVersion(t, "0.2.0")
	tests := []struct {
		st   update.State
		want string
	}{
		{update.State{}, "Not checked yet"},
		{update.State{Checking: true, LastCheck: at}, "Checking…"},
		{update.State{LastCheck: at}, "You have the latest version · checked 14:02"},
		{update.State{LastCheck: at, Latest: &update.Release{Version: v}}, "Version 0.2.0 is available · checked 14:02"},
		{update.State{LastCheck: at, CheckErr: "unavailable"}, "Release information isn't available · checked 14:02"},
		{update.State{LastCheck: at, CheckErr: "offline"}, "Couldn't reach GitHub · checked 14:02"},
		{update.State{LastCheck: at.AddDate(0, 0, -1), CheckErr: "boom"}, "Couldn't check for updates · checked 2026-10-23 14:02"},
	}
	for _, tt := range tests {
		if got := updateStatus(LangEN, tt.st, now); got != tt.want {
			t.Errorf("status = %q, want %q", got, tt.want)
		}
	}
}
