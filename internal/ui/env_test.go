package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/pkg/contracts"
)

// environmentNames must never reach the main window either: Settings and
// Diagnostics speak of "the system"; backends, desktops and probe reasons
// go only into the copied report.
var environmentNames = []string{"niri", "gnome", "kde", "plasma", "sway", "hyprland", "wayland", "x11", "xorg",
	"pipewire", "pulseaudio", "portal", "layer-shell", "screencopy", "globalshortcuts", "compositor", "композитор",
	"wlr-", "xdg-", "at-spi", "secret-service", "gio-window", "ipc+"}

func envReport() contracts.CapabilityReport {
	return contracts.CapabilityReport{
		OS: "linux", OSVersion: "Arch Linux (kernel 7.2.11)", Session: contracts.SessionWayland, Desktop: "niri", Compositor: "niri",
		Facts: map[string]string{"pipewire": "1.4.2"},
		Capabilities: map[contracts.Capability]contracts.CapabilityState{
			contracts.CapGlobalHotkey:         {Status: contracts.StatusDegraded, Backend: "ipc+compositor-binding", Reason: "GlobalShortcuts portal has no working backend on niri"},
			contracts.CapHotkeyRelease:        {Status: contracts.StatusUnavailable, Backend: "ipc+compositor-binding", Reason: "niri binds fire on key press only"},
			contracts.CapOverlayClickThrough:  {Status: contracts.StatusAvailable, Backend: "wlr-layer-shell"},
			contracts.CapOverlayFallbackPanel: {Status: contracts.StatusAvailable, Backend: "gio-window"},
			contracts.CapScreenCapture:        {Status: contracts.StatusAvailable, Backend: "wlr-screencopy"},
			contracts.CapInputInjection:       {Status: contracts.StatusAvailable, Backend: "xdg-portal:RemoteDesktop", Reason: "user consent is requested on first use"},
			contracts.CapAccessibility:        {Status: contracts.StatusAvailable, Backend: "at-spi2"},
			contracts.CapSecretStore:          {Status: contracts.StatusAvailable, Backend: "secret-service"},
			contracts.CapMicrophone:           {Status: contracts.StatusAvailable, Backend: "pipewire"},
			contracts.CapTray:                 {Status: contracts.StatusUnknown, Reason: "probe not implemented"},
		},
	}
}

func TestNoEnvironmentNamesInMainWindow(t *testing.T) {
	snap := diagSnapshot{report: envReport(), have: true, at: time.Now()}
	var texts []string
	for _, l := range []Lang{LangEN, LangRU} {
		texts = append(texts, diagTexts(l, snap)...)
		// Every main-window label, whatever is on screen right now.
		for k := range i18n.Keys() {
			for _, p := range []string{"nav.", "chat.", "settings.", "update.", "updates.", "platform.", "voice.", "tts.", "screen.", "privacy.", "memory.", "diag.", "cap.", "about.", "hotkey."} {
				if strings.HasPrefix(k, p) {
					texts = append(texts, T(l, k))
				}
			}
		}
	}
	for _, s := range texts {
		for _, n := range environmentNames {
			if strings.Contains(strings.ToLower(s), n) {
				t.Errorf("%q names %q", s, n)
			}
		}
	}
	// The details are not lost: they are in the report for support.
	js := string(reportJSON("1.0", snap))
	for _, want := range []string{`"desktop": "niri"`, "wlr-layer-shell", "GlobalShortcuts portal", `"app_version": "1.0"`} {
		if !strings.Contains(js, want) {
			t.Errorf("report lacks %q:\n%s", want, js)
		}
	}
}

var verbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(\.[0-9]+)?[a-zA-Z%]`)

// Russian text must keep the English format verbs, or Tf prints garbage.
func TestCatalogVerbsMatch(t *testing.T) {
	for k, en := range i18n.Keys() {
		if ru := i18n.T(i18n.RU, k); verbs(ru) != verbs(en) {
			t.Errorf("%s: ru verbs %q, en %q", k, verbs(ru), verbs(en))
		}
	}
}
