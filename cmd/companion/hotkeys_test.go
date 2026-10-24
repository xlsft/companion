package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/platform/hotkeys"
	"github.com/xlsft/companion/internal/platform/keybind"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/setupcheck"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// hotkeyCore is a core with only the hotkey parts: the control socket in a
// temp dir (no portal, no key grab) and desktop configs in another.
func hotkeyCore(t *testing.T, compositor, desktop string) (*core, string) {
	t.Helper()
	cfg := t.TempDir()
	old := hotkeyEnv
	hotkeyEnv = func() keybind.Env {
		return keybind.Env{ConfigHome: cfg, Getenv: func(string) string { return "" },
			Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	}
	t.Cleanup(func() { hotkeyEnv = old })
	report := contracts.CapabilityReport{Compositor: compositor, Desktop: desktop}
	sock := filepath.Join(t.TempDir(), "c.sock")
	hk, err := hotkeys.NewWithSocket(context.Background(), report, sock)
	if err != nil {
		t.Skipf("no control socket here: %v", err)
	}
	t.Cleanup(func() { hk.Close() })
	c := &core{log: slog.New(slog.DiscardHandler), settings: newSettings(t), report: report, hotkeys: hk}
	c.hkMu.Lock()
	if err := c.registerLocked(context.Background(), c.settings.Get()); err != nil {
		t.Fatal(err)
	}
	c.refreshAppliedLocked(context.Background())
	c.hkMu.Unlock()
	return c, cfg
}

func permission(c *core) string { return setupcheck.HotkeyPermission(c.hotkeyState()) }

func TestApplyHotkeysNiri(t *testing.T) {
	c, cfg := hotkeyCore(t, "niri", "niri")
	path := filepath.Join(cfg, "niri", "config.kdl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("binds {\n    Mod+T { spawn \"foot\"; }\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := permission(c); got != readiness.PermNeedsBinding {
		t.Fatalf("before apply: %s", got)
	}
	ctx := context.Background()

	tests := []struct {
		name, talk, cancel string
		state              wizard.HotkeyState
		code               string
		perm               string
	}{
		{"invalid chord", "space", "super+backspace", wizard.HotkeyFailed, "needs_modifier", readiness.PermNeedsBinding},
		{"taken by the user", "super+t", "super+backspace", wizard.HotkeyFailed, keybind.CodeConflict, readiness.PermNeedsBinding},
		{"applied", "Super+Space", "super+backspace", wizard.HotkeyApplied, "", readiness.PermGranted},
		{"changed and reapplied", "ctrl+alt+k", "super+backspace", wizard.HotkeyApplied, "", readiness.PermGranted},
	}
	for _, tt := range tests {
		before := c.settings.Get().Voice.TalkHotkey
		st := c.applyHotkeys(ctx, tt.talk, tt.cancel)
		if after := c.settings.Get().Voice.TalkHotkey; tt.state == wizard.HotkeyFailed && after != before {
			t.Errorf("%s: failed apply stored %q", tt.name, after)
		}
		if st.State != tt.state || st.Code != tt.code {
			t.Errorf("%s: %+v", tt.name, st)
		}
		if got := permission(c); got != tt.perm {
			t.Errorf("%s: permission %s, want %s", tt.name, got, tt.perm)
		}
		if tt.state == wizard.HotkeyApplied && !st.ToggleOnly {
			t.Errorf("%s: niri binds are press-only", tt.name)
		}
	}
	s := c.settings.Get().Voice
	if s.TalkHotkey != "ctrl+alt+k" || s.CancelHotkey != "super+backspace" {
		t.Errorf("stored %q %q", s.TalkHotkey, s.CancelHotkey)
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), ">>> companion"); n != 1 || !strings.Contains(string(b), "Ctrl+Alt+k") {
		t.Errorf("config:\n%s", b)
	}

	// A restart finds the block and counts it as applied.
	c2 := &core{log: c.log, settings: c.settings, report: c.report, hotkeys: c.hotkeys}
	c2.hkMu.Lock()
	c2.refreshAppliedLocked(ctx)
	c2.hkMu.Unlock()
	if st := c2.hotkeyStatus(); st.State != wizard.HotkeyApplied || st.Talk != "ctrl+alt+k" {
		t.Errorf("after restart: %+v", st)
	}
}

func TestApplyHotkeysManual(t *testing.T) {
	c, _ := hotkeyCore(t, "", "KDE")
	st := c.applyHotkeys(context.Background(), "super+space", "super+backspace")
	if st.State != wizard.HotkeyManual || len(st.Commands) != 2 || !strings.HasSuffix(st.Commands[0], " ctl talk-toggle") {
		t.Errorf("%+v", st)
	}
	if got := permission(c); got != readiness.PermNeedsBinding {
		t.Errorf("manual counts before a press: %s", got)
	}
	c.ctlSeen.Store(true) // a press through the user's own binding
	if got := permission(c); got != readiness.PermGranted {
		t.Errorf("press not counted: %s", got)
	}
}

func TestSettingsChangeRebinds(t *testing.T) {
	c, _ := hotkeyCore(t, "", "KDE")
	ctx := context.Background()
	if err := c.settings.Update(ctx, func(s *settings.Settings) { s.Voice.TalkHotkey = "ctrl+alt+j" }); err != nil {
		t.Fatal(err)
	}
	c.rebindHotkeys(ctx, c.settings.Get())
	if c.hkBound[0] != "ctrl+alt+j" {
		t.Errorf("bound %v", c.hkBound)
	}
}
