package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

func TestSetDoubleTap(t *testing.T) {
	tests := []struct {
		name, compositor, desktop string
		config                    string // relative path of a config to create
		supported                 bool
	}{
		{"sway binds taps", "sway", "sway", "sway/config", true},
		{"hyprland binds taps", "hyprland", "Hyprland", "hypr/hyprland.conf", true},
		{"niri cannot", "niri", "niri", "niri/config.kdl", false},
		{"manual cannot", "", "KDE", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cfg := hotkeyCore(t, tt.compositor, tt.desktop)
			var path string
			if tt.config != "" {
				path = filepath.Join(cfg, tt.config)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			p := setupPermissions{c}
			if key, ok := p.DoubleTap(); key != "" || ok != tt.supported {
				t.Fatalf("DoubleTap() = %q, %v", key, ok)
			}
			if err := p.SetDoubleTap(ctx, "space"); err == nil {
				t.Error("bad key accepted")
			}
			err := p.SetDoubleTap(ctx, "ctrl")
			if !tt.supported {
				if !errors.Is(err, contracts.ErrUnsupported) {
					t.Fatalf("unsupported system: %v", err)
				}
				if key, _ := p.DoubleTap(); key != "" {
					t.Errorf("stored %q on a system without double tap", key)
				}
				if err := p.SetDoubleTap(ctx, ""); err != nil {
					t.Errorf("turning off: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key, _ := p.DoubleTap(); key != "ctrl" {
				t.Errorf("stored %q", key)
			}
			b, _ := os.ReadFile(path)
			if strings.Count(string(b), "ctl tap-ctrl") != 2 {
				t.Errorf("config:\n%s", b)
			}
			if st := c.hotkeyStatus(); st.State != wizard.HotkeyApplied {
				t.Errorf("block with taps not counted as applied: %+v", st)
			}
			// Changing the chords keeps the tap binds.
			if st := c.applyHotkeys(ctx, "ctrl+alt+k", "super+backspace"); st.State != wizard.HotkeyApplied {
				t.Fatalf("apply chords: %+v", st)
			}
			if b, _ := os.ReadFile(path); strings.Count(string(b), "ctl tap-ctrl") != 2 {
				t.Errorf("chord change dropped the taps:\n%s", b)
			}
			if err := p.SetDoubleTap(ctx, ""); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "ctl tap-") {
				t.Errorf("off left tap binds:\n%s", b)
			}
			if key, _ := p.DoubleTap(); key != "" || c.hkTap != "" {
				t.Errorf("off: stored %q, armed %q", key, c.hkTap)
			}
		})
	}
}
