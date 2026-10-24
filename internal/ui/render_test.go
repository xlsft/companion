package ui

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/update"
	"github.com/xlsft/companion/pkg/contracts"
)

var frameSize = image.Pt(960, 640)

// frame lays out content once against router r, like a window frame.
func frame(r *input.Router, sh *Shell, th *theme.Theme, content surface) *op.Ops {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Source:      r.Source(),
		Constraints: layout.Exact(frameSize),
	}
	renderFrame(gtx, th, sh, content)
	r.Frame(ops)
	return ops
}

func reportWithCaps() contracts.CapabilityReport {
	return contracts.CapabilityReport{
		OS: "linux", Session: contracts.SessionWayland, Desktop: "niri",
		Capabilities: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapTray:          {Status: contracts.StatusAvailable, Backend: "sni"},
			contracts.CapGlobalHotkey:  {Status: contracts.StatusDegraded, Reason: "no key release"},
			contracts.CapScreenCapture: {Status: contracts.StatusUnavailable, Reason: "portal missing"},
			contracts.CapMicrophone:    {Status: contracts.StatusUnknown},
		},
	}
}

// TestLayoutAllScreens builds every window's widget tree in both
// languages, themes and the text scale extremes without a display.
func TestLayoutAllScreens(t *testing.T) {
	fake := &fakeUpdates{}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Updates = fake })
	banners := bannerStates(t)
	h.sh.diag = newDiagModel(func(context.Context) (contracts.CapabilityReport, error) { return reportWithCaps(), nil }, func() {})
	m, _ := h.mainBuilt()
	wiz := newWizardUI(h.sh)
	for _, lang := range []string{"ru", "en"} {
		if err := h.sh.opts.Settings.Update(context.Background(), func(s *settings.Settings) { s.UI.Language = lang }); err != nil {
			t.Fatal(err)
		}
		for _, dark := range []bool{false, true} {
			for _, scale := range []float32{theme.MinScale, 1, theme.MaxScale} {
				th := theme.New(dark, scale)
				h.sh.mu.Lock()
				h.sh.lang, h.sh.dark, h.sh.scale = langOf(lang), dark, scale
				h.sh.mu.Unlock()
				r := new(input.Router)
				m.navigate(RouteHome)
				frame(r, h.sh, th, m)
				for _, sec := range settingsSections {
					m.showSettings(sec)
					frame(r, h.sh, th, m)
					frame(r, h.sh, th, m)
				}
				m.about.show("t", "b")
				frame(r, h.sh, th, m)
				m.about.open = false
				for _, st := range banners {
					fake.set(st)
					frame(r, h.sh, th, m)
					frame(r, h.sh, th, wiz)
				}
				fake.set(update.State{})
				frame(r, h.sh, th, wiz)
				wiz.showDiagnostics(true)
				frame(r, h.sh, th, wiz)
				wiz.showDiagnostics(false)
			}
		}
	}
	if len(m.pages) != len(NavRoutes()) {
		t.Errorf("pages built = %d", len(m.pages))
	}
}

type recTarget struct {
	quit    chan struct{}
	scale   []int
	nav     []int
	newChat int
}

func (r *recTarget) Quit()             { r.quit <- struct{}{} }
func (r *recTarget) stepScale(n int)   { r.scale = append(r.scale, n) }
func (r *recTarget) navShortcut(d int) { r.nav = append(r.nav, d) }
func (r *recTarget) newChatShortcut()  { r.newChat++ }
func (r *recTarget) Layout(gtx layout.Context, _ *theme.Theme, _ Lang) layout.Dimensions {
	handleShortcuts(gtx, r)
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

func TestShortcuts(t *testing.T) {
	rt := &recTarget{quit: make(chan struct{}, 1)}
	r := new(input.Router)
	run := func() {
		gtx := layout.Context{Ops: new(op.Ops), Source: r.Source(), Constraints: layout.Exact(frameSize)}
		rt.Layout(gtx, nil, LangEN)
		r.Frame(gtx.Ops)
	}
	run()
	r.Queue(
		key.Event{Name: "=", Modifiers: key.ModShortcut, State: key.Press},
		key.Event{Name: "-", Modifiers: key.ModShortcut, State: key.Press},
		key.Event{Name: "0", Modifiers: key.ModShortcut, State: key.Press},
		key.Event{Name: "2", Modifiers: key.ModShortcut, State: key.Press},
		key.Event{Name: "3", Modifiers: key.ModShortcut, State: key.Press}, // no third reachable section
		key.Event{Name: "2", State: key.Press},                             // without Ctrl: ignored
		key.Event{Name: "N", Modifiers: key.ModShortcut, State: key.Press},
		key.Event{Name: "Q", Modifiers: key.ModShortcut, State: key.Press},
	)
	run()
	select {
	case <-rt.quit:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+Q did not quit")
	}
	if fmt.Sprint(rt.scale) != "[1 -1 0]" || fmt.Sprint(rt.nav) != "[2]" || rt.newChat != 1 {
		t.Errorf("scale=%v nav=%v newChat=%d", rt.scale, rt.nav, rt.newChat)
	}
}

// Tab must walk the sidebar in display order (FR-UI-007): About in the
// logo row, New chat, then the sections.
func TestTabOrderStartsWithNavigation(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	m, _ := h.mainBuilt()
	th := theme.New(false, 1)
	r := new(input.Router)
	frame(r, h.sh, th, m)
	r.MoveFocus(key.FocusForward)
	frame(r, h.sh, th, m)
	if !r.Source().Focused(&m.aboutBtn) {
		t.Fatal("Tab 1: About not focused")
	}
	r.MoveFocus(key.FocusForward)
	frame(r, h.sh, th, m)
	if !r.Source().Focused(&m.newChatBtn) {
		t.Fatal("Tab 2: New chat not focused")
	}
	// Soon sections are skipped: only reachable ones take focus.
	nav := NavRoutes()
	for i, route := range nav {
		r.MoveFocus(key.FocusForward)
		frame(r, h.sh, th, m)
		if !r.Source().Focused(m.navBtns[route]) {
			t.Fatalf("Tab %d: nav item %d not focused", i+3, route)
		}
	}
	// Enter activates the focused item like a click.
	r.Queue(key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	frame(r, h.sh, th, m)
	if m.current() != nav[len(nav)-1] {
		t.Errorf("route after Enter = %v", m.current())
	}
}

func TestEscClosesDialog(t *testing.T) {
	h := newHarness(t, ModeMain, nil)
	m, _ := h.mainBuilt()
	th := theme.New(false, 1)
	r := new(input.Router)
	m.about.show("About", "text")
	frame(r, h.sh, th, m)
	if !r.Source().Focused(&m.about.closeBtn) {
		t.Error("dialog button not focused on open")
	}
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	frame(r, h.sh, th, m)
	if m.about.open {
		t.Error("Esc did not close the dialog")
	}
}

// TestHeadlessRender draws real frames through Gio's GPU pipeline when an
// offscreen GL context is available. Set OC_UI_SNAPSHOT_DIR to keep PNGs.
func TestHeadlessRender(t *testing.T) {
	w, err := headless.NewWindow(frameSize.X, frameSize.Y)
	if err != nil {
		t.Skipf("no headless GPU context on this machine: %v", err)
	}
	defer w.Release()

	fake := &fakeUpdates{}
	h := newHarnessWith(t, ModeMain, nil, func(o *Options) { o.Updates = fake })
	h.sh.diag = newDiagModel(func(context.Context) (contracts.CapabilityReport, error) { return reportWithCaps(), nil }, func() {})
	h.sh.diag.Refresh()
	banners := bannerStates(t)
	for _, running := true, true; running; {
		running = h.sh.diag.snapshot().running
		time.Sleep(time.Millisecond)
	}
	m, _ := h.mainBuilt()
	wiz := newWizardUI(h.sh)
	th := theme.New(false, 1)
	shots := []struct {
		name    string
		content surface
		route   Route
		section settingsSection
		open    bool // the language select is open
		diag    bool // the setup window shows Diagnostics
		upd     string
	}{
		{"wizard", wiz, 0, 0, false, false, ""},
		{"wizard-diagnostics", wiz, 0, 0, false, true, ""},
		{"home", m, RouteHome, 0, false, false, ""},
		{"settings", m, RouteSettings, secGeneral, false, false, ""},
		{"settings-language", m, RouteSettings, secGeneral, true, false, ""},
		{"settings-voice", m, RouteSettings, secVoice, false, false, ""},
		{"diagnostics", m, RouteSettings, secDiagnostics, false, false, ""},
		{"update-home", m, RouteHome, 0, false, false, "available"},
		{"update-settings", m, RouteSettings, secGeneral, false, false, "downloading"},
		{"update-wizard", wiz, 0, 0, false, false, "available"},
		{"update-failed", m, RouteHome, 0, false, false, "failed"},
		{"update-managed", wiz, 0, 0, false, false, "managed"},
		{"update-restart", m, RouteSettings, secDiagnostics, false, false, "restart"},
	}
	dir := os.Getenv("OC_UI_SNAPSHOT_DIR")
	variants := []struct {
		suffix string
		dark   bool
		scale  float32
	}{{"", false, 1}, {"-dark-x2", true, theme.MaxScale}}
	for _, v := range variants {
		h.sh.mu.Lock()
		h.sh.dark, h.sh.scale = v.dark, v.scale
		h.sh.mu.Unlock()
		for _, s := range shots {
			s.name += v.suffix
			m.navigate(s.route)
			if s.route == RouteSettings {
				m.showSettings(s.section)
			}
			wiz.showDiagnostics(s.diag)
			fake.set(banners[s.upd])
			r := new(input.Router)
			frame(r, h.sh, th, s.content)
			if sp, ok := m.pages[RouteSettings].(*settingsPage); ok {
				sp.uiLang.SetOpen(s.open)
			}
			ops := frame(r, h.sh, th, s.content) // second frame: lazily built pages settle
			if err := w.Frame(ops); err != nil {
				t.Fatalf("%s: frame: %v", s.name, err)
			}
			img := image.NewRGBA(image.Rectangle{Max: frameSize})
			if err := w.Screenshot(img); err != nil {
				t.Fatalf("%s: screenshot: %v", s.name, err)
			}
			if uniform(img) {
				t.Errorf("%s: frame is a single colour", s.name)
			}
			if dir != "" {
				f, err := os.Create(filepath.Join(dir, s.name+".png"))
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, img); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
		}
	}
}

func uniform(img *image.RGBA) bool {
	first := img.RGBAAt(0, 0)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			if img.RGBAAt(x, y) != first {
				return false
			}
		}
	}
	return true
}

// bannerStates are the update banner's looks, by name; "" is no banner.
func bannerStates(t *testing.T) map[string]update.State {
	avail := availableState(t)
	with := func(fn func(*update.State)) update.State {
		st := avail
		fn(&st)
		return st
	}
	return map[string]update.State{
		"":            {Current: avail.Current, LastCheck: avail.LastCheck},
		"available":   avail,
		"managed":     with(func(s *update.State) { s.Managed = "package_managed" }),
		"downloading": with(func(s *update.State) { s.Phase, s.Done, s.Total = update.PhaseDownloading, 12<<20, 40<<20 }),
		"verifying":   with(func(s *update.State) { s.Phase = update.PhaseVerifying }),
		"failed":      with(func(s *update.State) { s.Phase, s.Err = update.PhaseFailed, "bad_signature" }),
		"restart":     with(func(s *update.State) { s.Phase = update.PhaseRestartNeeded }),
	}
}
