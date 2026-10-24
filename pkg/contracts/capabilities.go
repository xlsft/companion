package contracts

import "context"

// Capability names one OS-level function the core may rely on.
type Capability string

const (
	CapGlobalHotkey         Capability = "global_hotkey"
	CapHotkeyRelease        Capability = "hotkey_release" // hold-to-talk needs key-up events
	CapScreenCapture        Capability = "screen_capture"
	CapOverlayClickThrough  Capability = "overlay_click_through"
	CapOverlayFallbackPanel Capability = "overlay_fallback_panel"
	CapInputInjection       Capability = "input_injection"
	CapAccessibility        Capability = "accessibility"
	CapTextInsertion        Capability = "text_insertion"
	CapSecretStore          Capability = "secret_store"
	CapMicrophone           Capability = "microphone"
	CapTray                 Capability = "tray"
)

// Status is how usable a capability is right now.
type Status string

const (
	StatusAvailable       Status = "available"
	StatusDegraded        Status = "degraded" // works with limitations, see Reason
	StatusNeedsPermission Status = "needs_permission"
	StatusUnavailable     Status = "unavailable" // use Fallback
	StatusUnknown         Status = "unknown"     // not probed yet
)

// CapabilityState is the detected state of one capability.
type CapabilityState struct {
	Status Status
	// Backend names the mechanism in use, e.g. "wlr-layer-shell",
	// "xdg-portal:GlobalShortcuts", "windows-graphics-capture".
	Backend string
	// Reason is a short technical explanation for non-available states. It
	// never contains user content.
	Reason string
	// Fallback names the capability or UX used instead, if any.
	Fallback Capability
}

// SessionKind is the windowing system family.
type SessionKind string

const (
	SessionWindows SessionKind = "windows"
	SessionMacOS   SessionKind = "macos"
	SessionX11     SessionKind = "x11"
	SessionWayland SessionKind = "wayland"
)

// CapabilityReport is the result of runtime platform detection (OC-13).
type CapabilityReport struct {
	OS         string // runtime.GOOS
	OSVersion  string
	Session    SessionKind
	Desktop    string // e.g. "GNOME", "KDE", "niri"; empty if unknown
	Compositor string
	// Facts holds raw probe results for diagnostics, e.g.
	// "portal.GlobalShortcuts.version" -> "1", "pipewire" -> "1.4.2".
	Facts        map[string]string
	Capabilities map[Capability]CapabilityState
}

// State returns the state of c, or StatusUnknown if it was not probed.
func (r CapabilityReport) State(c Capability) CapabilityState {
	if s, ok := r.Capabilities[c]; ok {
		return s
	}
	return CapabilityState{Status: StatusUnknown}
}

// CapabilityDetector probes the running system. Detect must be cheap enough
// to call at startup and when diagnostics are opened; it must not trigger
// permission prompts.
type CapabilityDetector interface {
	Detect(ctx context.Context) (CapabilityReport, error)
}
