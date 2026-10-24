package wizard

import (
	"context"
	"testing"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
)

// environmentNames must never reach the screen: the wizard speaks of "the
// system", whatever the platform reports (backends, reasons, desktop).
var environmentNames = []string{"niri", "gnome", "kde", "plasma", "sway", "hyprland", "wayland", "x11", "xorg",
	"pipewire", "pulseaudio", "portal", "layer-shell", "screencopy", "globalshortcuts", "compositor", "композитор"}

func TestNoEnvironmentNames(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.RU} {
		h := newHarness(t)
		h.lang = lang
		h.checks.set(readiness.StepPermissions, readiness.Result{Status: readiness.Failed, Code: readiness.PermNeedsBinding,
			Detail: map[string]string{"global_hotkey": readiness.PermNeedsBinding, "overlay_click_through": readiness.PermFallback}})
		if _, err := h.gate.Run(context.Background(), readiness.StepPermissions); err != nil {
			t.Fatal(err)
		}
		for i, st := range readiness.Steps {
			if i > 0 {
				h.pass(readiness.Steps[i-1])
			}
			h.eventually("step "+string(st), func() bool { return h.view() == st })
			if st == readiness.StepPlatform {
				h.eventually("report", func() bool { return h.shows(i18n.T(lang, "cap.col.means")) })
			}
			for _, s := range h.w.frame.texts {
				for _, n := range environmentNames {
					if contains(s, n) {
						t.Errorf("%s/%s: %q names %q", lang, st, s, n)
					}
				}
			}
		}
	}
}
