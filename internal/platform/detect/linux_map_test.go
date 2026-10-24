package detect

import (
	"testing"

	"github.com/xlsft/openclick/pkg/contracts"
)

func portals(gs, sc, rd bool) map[string]portalProbe {
	m := map[string]portalProbe{}
	for iface, ok := range map[string]bool{portalGlobalShortcuts: gs, portalScreenCast: sc, portalRemoteDesktop: rd} {
		if ok {
			m[iface] = portalProbe{Present: true, Version: 1}
		} else {
			m[iface] = portalProbe{Err: "org.freedesktop.DBus.Error.UnknownInterface"}
		}
	}
	return m
}

func TestMapLinux(t *testing.T) {
	layer := []Global{{Name: 1, Interface: globLayerShell, Version: 4}, {Name: 2, Interface: globScreencopy, Version: 3}}
	shot := portals(false, true, false)
	shot[portalScreenshot] = portalProbe{Present: true, Version: 2}
	captureBackend := func(want string) func(t *testing.T, r contracts.CapabilityReport) {
		return func(t *testing.T, r contracts.CapabilityReport) {
			if got := r.State(contracts.CapScreenCapture).Backend; got != want {
				t.Errorf("screen_capture backend = %q, want %q", got, want)
			}
		}
	}
	tests := []struct {
		name  string
		facts linuxFacts
		want  map[contracts.Capability]contracts.Status
		extra func(t *testing.T, r contracts.CapabilityReport)
	}{
		{
			name: "x11",
			facts: linuxFacts{SessionType: "x11", X11Display: ":0", Desktop: "XFCE", Portals: portals(false, false, false),
				ATSPI: busName{Owned: true}, SecretService: busName{Owned: true}},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapGlobalHotkey: contracts.StatusAvailable, contracts.CapHotkeyRelease: contracts.StatusAvailable,
				contracts.CapScreenCapture: contracts.StatusAvailable, contracts.CapOverlayClickThrough: contracts.StatusAvailable,
				contracts.CapInputInjection: contracts.StatusAvailable, contracts.CapAccessibility: contracts.StatusAvailable,
				contracts.CapSecretStore: contracts.StatusAvailable, contracts.CapOverlayFallbackPanel: contracts.StatusAvailable,
			},
			extra: func(t *testing.T, r contracts.CapabilityReport) {
				if r.Session != contracts.SessionX11 || r.Desktop != "XFCE" {
					t.Errorf("session/desktop = %v/%q", r.Session, r.Desktop)
				}
			},
		},
		{
			name: "wayland full",
			facts: linuxFacts{SessionType: "wayland", WaylandDisplay: "wayland-1", Niri: true, Desktop: "niri",
				Portals: portals(true, true, true), PipeWire: true, WaylandChecked: true, Globals: layer,
				ATSPI: busName{Activatable: true}, SecretService: busName{Owned: true}},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapGlobalHotkey: contracts.StatusAvailable, contracts.CapHotkeyRelease: contracts.StatusUnknown,
				contracts.CapScreenCapture: contracts.StatusAvailable, contracts.CapOverlayClickThrough: contracts.StatusAvailable,
				contracts.CapInputInjection: contracts.StatusAvailable, contracts.CapAccessibility: contracts.StatusAvailable,
				contracts.CapSecretStore: contracts.StatusAvailable, contracts.CapMicrophone: contracts.StatusAvailable,
			},
			extra: func(t *testing.T, r contracts.CapabilityReport) {
				if r.Compositor != "niri" || r.Session != contracts.SessionWayland {
					t.Errorf("compositor/session = %q/%v", r.Compositor, r.Session)
				}
				if r.Facts["portal.GlobalShortcuts.version"] != "1" || r.Facts["wayland.layer_shell"] != "true" ||
					r.Facts["wayland.screencopy"] != "true" {
					t.Errorf("facts = %v", r.Facts)
				}
				captureBackend("wlr-screencopy")(t, r)
			},
		},
		{
			name: "wayland no layer shell no portals",
			facts: linuxFacts{SessionType: "wayland", WaylandDisplay: "wayland-0", Desktop: "GNOME", Portals: portals(false, false, false),
				WaylandChecked: true, Globals: []Global{{Interface: "wl_compositor", Version: 6}}},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapGlobalHotkey: contracts.StatusUnavailable, contracts.CapHotkeyRelease: contracts.StatusUnavailable,
				contracts.CapScreenCapture: contracts.StatusUnavailable, contracts.CapOverlayClickThrough: contracts.StatusUnavailable,
				contracts.CapInputInjection: contracts.StatusUnavailable, contracts.CapAccessibility: contracts.StatusUnavailable,
				contracts.CapSecretStore: contracts.StatusUnavailable, contracts.CapOverlayFallbackPanel: contracts.StatusAvailable,
			},
			extra: func(t *testing.T, r contracts.CapabilityReport) {
				if got := r.State(contracts.CapOverlayClickThrough).Fallback; got != contracts.CapOverlayFallbackPanel {
					t.Errorf("fallback = %q", got)
				}
				if r.Compositor != "mutter" {
					t.Errorf("compositor = %q", r.Compositor)
				}
			},
		},
		{
			name: "wayland screencast without pipewire",
			facts: linuxFacts{SessionType: "wayland", WaylandDisplay: "wayland-0", Portals: portals(true, true, false),
				WaylandChecked: true, WaylandErr: "registry probe failed"},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapScreenCapture: contracts.StatusUnavailable, contracts.CapOverlayClickThrough: contracts.StatusUnknown,
				contracts.CapInputInjection: contracts.StatusUnavailable,
			},
		},
		{
			name: "wayland screenshot portal without screencopy",
			facts: linuxFacts{SessionType: "wayland", WaylandDisplay: "wayland-0", Desktop: "GNOME", Portals: shot,
				PipeWire: true, WaylandChecked: true, Globals: []Global{{Interface: "wl_compositor", Version: 6}}},
			want:  map[contracts.Capability]contracts.Status{contracts.CapScreenCapture: contracts.StatusAvailable},
			extra: captureBackend("xdg-portal:Screenshot"),
		},
		{
			name: "wayland screencast only is not a backend yet",
			facts: linuxFacts{SessionType: "wayland", WaylandDisplay: "wayland-0", Portals: portals(false, true, false),
				PipeWire: true, WaylandChecked: true},
			want: map[contracts.Capability]contracts.Status{contracts.CapScreenCapture: contracts.StatusUnavailable},
		},
		{
			name:  "session from WAYLAND_DISPLAY when type unset; bus down",
			facts: linuxFacts{WaylandDisplay: "wayland-0", PortalBusErr: "connect failed", Portals: map[string]portalProbe{}},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapGlobalHotkey: contracts.StatusUnavailable, contracts.CapSecretStore: contracts.StatusUnknown,
				contracts.CapAccessibility: contracts.StatusUnknown,
			},
			extra: func(t *testing.T, r contracts.CapabilityReport) {
				if r.Session != contracts.SessionWayland {
					t.Errorf("session = %v", r.Session)
				}
			},
		},
		{
			name:  "headless",
			facts: linuxFacts{Portals: map[string]portalProbe{}},
			want: map[contracts.Capability]contracts.Status{
				contracts.CapGlobalHotkey: contracts.StatusUnavailable, contracts.CapOverlayFallbackPanel: contracts.StatusUnavailable,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mapLinux(tt.facts)
			for c, want := range tt.want {
				if got := r.State(c).Status; got != want {
					t.Errorf("%s = %s (%s), want %s", c, got, r.State(c).Reason, want)
				}
			}
			for c, s := range r.Capabilities {
				if s.Status != contracts.StatusAvailable && s.Reason == "" {
					t.Errorf("%s: non-available state without reason", c)
				}
			}
			if tt.extra != nil {
				tt.extra(t, r)
			}
		})
	}
}
