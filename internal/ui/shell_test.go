package ui

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// stubHotkeys reports applied shortcuts and accepts every apply.
type stubHotkeys struct{}

func (stubHotkeys) Hotkeys() wizard.HotkeyStatus {
	return wizard.HotkeyStatus{Talk: "super+space", Cancel: "super+backspace", State: wizard.HotkeyApplied, ToggleOnly: true}
}

func (stubHotkeys) ApplyHotkeys(_ context.Context, talk, cancel string) wizard.HotkeyStatus {
	return wizard.HotkeyStatus{Talk: talk, Cancel: cancel, State: wizard.HotkeyApplied}
}

type fakeWindow struct {
	spec                       windowSpec
	invalidated, raised, close atomic.Int32
}

func (w *fakeWindow) Invalidate() { w.invalidated.Add(1) }
func (w *fakeWindow) Raise()      { w.raised.Add(1) }
func (w *fakeWindow) Close()      { w.close.Add(1) }

// userClose simulates the user closing the window from the title bar.
func (w *fakeWindow) userClose() { w.spec.OnClosed(w) }

type fakeWindows struct {
	mu   sync.Mutex
	wins []*fakeWindow
}

func (f *fakeWindows) factory(spec windowSpec) window {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWindow{spec: spec}
	f.wins = append(f.wins, w)
	return w
}

func (f *fakeWindows) all() []*fakeWindow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.wins)
}

type fakeTray struct {
	startErr error
	mu       sync.Mutex
	items    []contracts.TrayMenuItem
	sets     int
	onSelect func(string)
	stopped  bool
}

func (t *fakeTray) Start(_ context.Context, _ string, items []contracts.TrayMenuItem, onSelect func(string)) error {
	if t.startErr != nil {
		return t.startErr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items, t.onSelect = items, onSelect
	return nil
}

func (t *fakeTray) SetItems(items []contracts.TrayMenuItem) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = items
	t.sets++
	return nil
}

func (t *fakeTray) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	return nil
}

func (t *fakeTray) snapshot() []contracts.TrayMenuItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.items)
}

func (t *fakeTray) click(id string) { t.onSelect(id) }

func newSettings(t *testing.T) *settings.Service {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type harness struct {
	sh    *Shell
	wins  *fakeWindows
	tray  *fakeTray
	quits atomic.Int32
	gate  *readiness.Gate
	pass  atomic.Bool // the gate's checks pass
}

type memProgress struct {
	mu   sync.Mutex
	data []byte
}

func (m *memProgress) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, nil
}

func (m *memProgress) Save(_ context.Context, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = d
	return nil
}

func newHarness(t *testing.T, mode Mode, tray *fakeTray) *harness {
	t.Helper()
	return newHarnessWith(t, mode, tray, nil)
}

func newHarnessWith(t *testing.T, mode Mode, tray *fakeTray, mut func(*Options)) *harness {
	t.Helper()
	h := &harness{wins: &fakeWindows{}, tray: tray}
	checks := map[readiness.Step]readiness.Check{}
	for _, st := range readiness.Steps {
		checks[st] = func(context.Context) readiness.Result {
			if h.pass.Load() {
				return readiness.Result{Status: readiness.Passed}
			}
			return readiness.Result{Status: readiness.Failed, Code: "not_wired"}
		}
	}
	g, err := readiness.Open(context.Background(), &memProgress{}, checks)
	if err != nil {
		t.Fatal(err)
	}
	h.gate = g
	opts := Options{
		Gate:     g,
		Mode:     mode,
		Settings: newSettings(t),
		Setup:    wizard.Backends{Permissions: stubHotkeys{}},
		Quit:     func() { h.quits.Add(1) },
		Diagnose: func(context.Context) (contracts.CapabilityReport, error) {
			return contracts.CapabilityReport{OS: "linux"}, nil
		},
	}
	if tray != nil {
		opts.Tray = tray
	}
	if mut != nil {
		mut(&opts)
	}
	h.sh = newShell(opts, h.wins.factory)
	if err := h.sh.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.sh.Stop(context.Background()) })
	return h
}

func (h *harness) mainBuilt() (*mainUI, int) {
	h.sh.mu.Lock()
	defer h.sh.mu.Unlock()
	return h.sh.main, h.sh.mainBuilds
}

func contentKinds(ws []*fakeWindow) []string {
	var out []string
	for _, w := range ws {
		switch w.spec.Content.(type) {
		case *wizardUI:
			out = append(out, "wizard")
		case *mainUI:
			out = append(out, "main")
		default:
			out = append(out, "other")
		}
	}
	return out
}

func TestSetupModeBuildsOnlyWizard(t *testing.T) {
	h := newHarness(t, ModeSetup, &fakeTray{})
	if got := contentKinds(h.wins.all()); !slices.Equal(got, []string{"wizard"}) {
		t.Fatalf("windows = %v", got)
	}
	// Nothing reachable in setup mode may construct the main UI.
	h.tray.click(TrayOpen)
	h.tray.click(TrayDiagnostics)
	h.sh.navShortcut(1)
	if m, n := h.mainBuilt(); m != nil || n != 0 {
		t.Fatalf("main UI built in setup mode (%d)", n)
	}
	wins := h.wins.all()
	if len(wins) != 1 || wins[0].raised.Load() == 0 {
		t.Errorf("tray open should raise the wizard, windows=%d", len(wins))
	}
	if !h.sh.wizard.diagnosticsShown() {
		t.Error("tray diagnostics did not switch the wizard to diagnostics")
	}
}

func TestTrayItemsPerMode(t *testing.T) {
	for _, m := range []Mode{ModeSetup, ModeMain} {
		items := TrayItems(m, LangEN)
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ID)
			low := strings.ToLower(it.ID + it.Label)
			if m == ModeSetup && (strings.Contains(low, "talk") || strings.Contains(low, "chat")) {
				t.Errorf("setup tray offers %q (FR-ONB-003)", it.Label)
			}
		}
		if !slices.Equal(ids, []string{TrayOpen, TrayDiagnostics, TrayQuit}) {
			t.Errorf("%v items = %v", m, ids)
		}
	}
}

func TestEnterMain(t *testing.T) {
	h := newHarness(t, ModeSetup, &fakeTray{})
	wiz := h.wins.all()[0]
	if err := h.sh.EnterMain(); err != nil {
		t.Fatal(err)
	}
	if wiz.close.Load() != 1 {
		t.Errorf("wizard close calls = %d", wiz.close.Load())
	}
	// The real window reports its destruction afterwards; that close was
	// the shell's, so it must not quit even though windows changed.
	wiz.userClose()
	if got := contentKinds(h.wins.all()); !slices.Equal(got, []string{"wizard", "main"}) {
		t.Fatalf("windows = %v", got)
	}
	if h.sh.Mode() != ModeMain || h.sh.wizard != nil {
		t.Error("still in setup or wizard UI kept")
	}
	if h.tray.sets != 1 || h.tray.snapshot()[0].Label != T(LangEN, "tray.open") {
		t.Errorf("tray not switched to main items: %+v", h.tray.snapshot())
	}
	if err := h.sh.EnterMain(); err != nil {
		t.Fatal(err)
	}
	if _, n := h.mainBuilt(); n != 1 || len(h.wins.all()) != 2 || h.quits.Load() != 0 {
		t.Errorf("second EnterMain: builds=%d windows=%d quits=%d", n, len(h.wins.all()), h.quits.Load())
	}
}

func TestMainModeIsLazy(t *testing.T) {
	h := newHarness(t, ModeMain, &fakeTray{})
	m, n := h.mainBuilt()
	if m == nil || n != 1 {
		t.Fatalf("main UI not built once: %d", n)
	}
	if len(m.pages) != 0 {
		t.Errorf("pages built before first frame: %d", len(m.pages))
	}
	m.page(RouteSettings)
	m.page(RouteSettings)
	if len(m.pages) != 1 {
		t.Errorf("pages = %d", len(m.pages))
	}
}

// Sections marked Soon are not reachable by any path, and there are no
// placeholder pages behind them.
func TestSoonRoutesUnreachable(t *testing.T) {
	h := newHarness(t, ModeMain, &fakeTray{})
	m, _ := h.mainBuilt()
	for _, r := range []Route{RouteHistory, RouteAgents, RouteSkills} {
		if r.Implemented() {
			t.Errorf("route %d should be Soon", r)
		}
		h.sh.Navigate(r)
		if m.current() != RouteHome {
			t.Errorf("Navigate(%d) reached %v", r, m.current())
		}
		if _, ok := m.navBtns[r]; ok {
			t.Errorf("route %d has a nav button", r)
		}
	}
	for d := 1; d <= 9; d++ {
		if r, ok := RouteForShortcut(d); ok && !r.Implemented() {
			t.Errorf("Ctrl+%d reaches %d", d, r)
		}
	}
}

func TestCloseWithTrayKeepsRunning(t *testing.T) {
	h := newHarness(t, ModeMain, &fakeTray{})
	first := h.wins.all()[0]
	first.userClose()
	if h.quits.Load() != 0 {
		t.Fatal("quit after closing main window although tray is available")
	}
	h.tray.click(TrayOpen)
	wins := h.wins.all()
	if len(wins) != 2 || wins[1].spec.Content != first.spec.Content {
		t.Fatalf("open should re-create the window with the same UI, windows=%d", len(wins))
	}
	if _, n := h.mainBuilt(); n != 1 {
		t.Errorf("main UI rebuilt: %d", n)
	}
	h.tray.click(TrayOpen)
	if len(h.wins.all()) != 2 || wins[1].raised.Load() != 1 {
		t.Error("open on a visible window should raise it")
	}
	h.tray.click(TrayDiagnostics)
	if m, _ := h.mainBuilt(); m.current() != RouteSettings {
		t.Errorf("route = %v", m.current())
	} else if s, ok := m.takeSection(); !ok || s != secDiagnostics {
		t.Errorf("section = %v %v", s, ok)
	}
	h.tray.click(TrayQuit)
	h.tray.click(TrayQuit)
	if h.quits.Load() != 1 {
		t.Errorf("quits = %d", h.quits.Load())
	}
}

func TestCloseWithoutTrayQuits(t *testing.T) {
	for _, mode := range []Mode{ModeSetup, ModeMain} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newHarness(t, mode, &fakeTray{startErr: contracts.Unsupported(contracts.CapTray)})
			if h.sh.TrayAvailable() {
				t.Fatal("tray reported available")
			}
			h.wins.all()[0].userClose()
			if h.quits.Load() != 1 {
				t.Errorf("quits = %d", h.quits.Load())
			}
		})
	}
}

func TestEnterMainWithoutTrayDoesNotQuit(t *testing.T) {
	h := newHarness(t, ModeSetup, nil)
	wiz := h.wins.all()[0]
	if err := h.sh.EnterMain(); err != nil {
		t.Fatal(err)
	}
	wiz.userClose()
	if h.quits.Load() != 0 {
		t.Error("programmatic wizard close quit the app")
	}
}

func TestStopClosesEverything(t *testing.T) {
	h := newHarness(t, ModeMain, &fakeTray{})
	w := h.wins.all()[0]
	if err := h.sh.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.close.Load() != 1 || !h.tray.stopped {
		t.Errorf("close=%d trayStopped=%v", w.close.Load(), h.tray.stopped)
	}
	w.userClose()
	h.sh.Open()
	if h.quits.Load() != 0 || len(h.wins.all()) != 1 {
		t.Error("window activity after Stop")
	}
	if err := h.sh.EnterMain(); err != nil {
		t.Error(err) // already main: no-op
	}
}

func TestLanguageChangeRelabelsTray(t *testing.T) {
	h := newHarness(t, ModeMain, &fakeTray{})
	if err := h.sh.opts.Settings.Update(context.Background(), func(s *settings.Settings) { s.UI.Language = "en" }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.tray.snapshot()[0].Label != "Open" {
		if time.Now().After(deadline) {
			t.Fatalf("tray labels = %+v", h.tray.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l, _, _ := h.sh.look(); l != LangEN {
		t.Errorf("lang = %v", l)
	}
}

func TestNavShortcutAndScale(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	h.sh.navShortcut(2)
	if m, _ := h.mainBuilt(); m.current() != RouteSettings {
		t.Errorf("route = %v", m.current())
	}
	h.sh.stepScale(1)
	h.sh.stepScale(1)
	if _, _, s := h.sh.look(); s != 1.25 {
		t.Errorf("scale = %v", s)
	}
	for i := 0; i < 20; i++ {
		h.sh.stepScale(-1)
	}
	if _, _, s := h.sh.look(); s != 0.75 {
		t.Errorf("scale = %v", s)
	}
	h.sh.stepScale(0)
	if _, _, s := h.sh.look(); s != 1 {
		t.Errorf("scale = %v", s)
	}
}

// The only way out of setup is the Gate becoming Ready (FR-ONB-002/010).
func TestReadyGateEntersMainOnce(t *testing.T) {
	h := newHarness(t, ModeSetup, &fakeTray{})
	ctx := context.Background()
	if _, err := h.gate.Run(ctx, readiness.StepPermissions); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if h.sh.Mode() != ModeSetup {
		t.Fatal("a failed check left setup")
	}
	h.pass.Store(true)
	for _, st := range readiness.Steps {
		if _, err := h.gate.Run(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.sh.Mode() != ModeMain {
		if time.Now().After(deadline) {
			t.Fatal("ready gate did not enter main mode")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, n := h.mainBuilt(); n != 1 {
		t.Errorf("main builds = %d", n)
	}
	if got := contentKinds(h.wins.all()); !slices.Equal(got, []string{"wizard", "main"}) {
		t.Errorf("windows = %v", got)
	}
}

func TestStartRequiresGateInSetup(t *testing.T) {
	sh := newShell(Options{Mode: ModeSetup, Settings: newSettings(t)}, (&fakeWindows{}).factory)
	if err := sh.Start(context.Background()); err == nil {
		t.Error("expected error without a Gate")
	}
}

func TestStartRequiresSettings(t *testing.T) {
	sh := newShell(Options{}, (&fakeWindows{}).factory)
	if err := sh.Start(context.Background()); err == nil {
		t.Error("expected error")
	}
}

type fakeScheme struct {
	mu   sync.Mutex
	dark bool
	ch   chan struct{}
}

func (f *fakeScheme) Start(context.Context) error { return nil }
func (f *fakeScheme) Dark() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dark
}
func (f *fakeScheme) Changes() <-chan struct{} { return f.ch }
func (f *fakeScheme) flip(dark bool) {
	f.mu.Lock()
	f.dark = dark
	f.mu.Unlock()
	f.ch <- struct{}{}
}

// TestFollowsSystemScheme: the shell starts in the system scheme and
// repaints its windows when it changes, with no restart.
func TestFollowsSystemScheme(t *testing.T) {
	src := &fakeScheme{dark: true, ch: make(chan struct{}, 1)}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Appearance = src })
	if _, dark, _ := h.sh.look(); !dark {
		t.Fatal("did not start dark")
	}
	w := h.wins.all()[0]
	before := w.invalidated.Load()
	src.flip(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, dark, _ := h.sh.look()
		if !dark && w.invalidated.Load() > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not follow the scheme change")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNoAppearanceIsLight(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	if _, dark, _ := h.sh.look(); dark {
		t.Error("dark without a source")
	}
}
