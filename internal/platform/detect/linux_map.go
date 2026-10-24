package detect

import (
	"sort"
	"strconv"
	"strings"

	"github.com/xlsft/companion/pkg/contracts"
)

// Portal interface names probed under /org/freedesktop/portal/desktop.
const (
	portalGlobalShortcuts = "org.freedesktop.portal.GlobalShortcuts"
	portalScreenCast      = "org.freedesktop.portal.ScreenCast"
	portalRemoteDesktop   = "org.freedesktop.portal.RemoteDesktop"
	portalScreenshot      = "org.freedesktop.portal.Screenshot"
)

// Wayland globals that matter for capabilities.
const (
	globLayerShell      = "zwlr_layer_shell_v1"
	globExtToplevelList = "ext_foreign_toplevel_list_v1"
	globWlrToplevelMgr  = "zwlr_foreign_toplevel_manager_v1"
	globScreencopy      = "zwlr_screencopy_manager_v1"
	globVirtualKeyboard = "zwp_virtual_keyboard_manager_v1"
)

// BackendVirtualKeyboard types through zwp_virtual_keyboard_v1, for
// compositors whose RemoteDesktop portal has no keyboard (niri, OC-6).
const BackendVirtualKeyboard = "wayland-virtual-keyboard"

// Hotkey backend names reported in CapabilityState.Backend; the chooser in
// internal/platform/hotkeys reports the same names at runtime.
const (
	BackendX11Grab = "x11-grabkey"
	BackendPortal  = "xdg-portal:GlobalShortcuts"
	BackendIPC     = "ipc+compositor-binding"
)

// ipcHotkey is the control-socket fallback: it always works, but only after
// the user binds a compositor key to "companion ctl".
func ipcHotkey(why string) contracts.CapabilityState {
	return contracts.CapabilityState{
		Status: contracts.StatusDegraded, Backend: BackendIPC,
		Reason: why + "; bind a compositor key to companion ctl",
	}
}

// portalProbe is the outcome of reading one portal interface's version.
type portalProbe struct {
	Present bool
	Version uint32
	Err     string // short technical reason when not present
	// Devices is RemoteDesktop's AvailableDeviceTypes bit set (1 keyboard,
	// 2 pointer, 4 touchscreen), when DevicesKnown.
	Devices      uint32
	DevicesKnown bool
}

// busName is the outcome of checking a well-known bus name.
type busName struct {
	Owned       bool
	Activatable bool
	Err         string
}

func (b busName) usable() bool { return b.Owned || b.Activatable }

// linuxFacts is everything the Linux probes learned. Mapping from facts to a
// report is pure so it can be table-tested without a desktop session.
type linuxFacts struct {
	OSVersion string

	SessionType    string // XDG_SESSION_TYPE
	WaylandDisplay string
	X11Display     string
	Desktop        string // XDG_CURRENT_DESKTOP
	Niri           bool
	Hyprland       bool
	Sway           bool

	Portals       map[string]portalProbe
	PortalBusErr  string // set when the session bus was unreachable
	SecretService busName
	ATSPI         busName
	PipeWire      bool

	WaylandChecked bool // a registry listing was attempted
	WaylandErr     string
	Globals        []Global
}

func (f linuxFacts) hasGlobal(name string) bool {
	for _, g := range f.Globals {
		if g.Interface == name {
			return true
		}
	}
	return false
}

func sessionKind(f linuxFacts) contracts.SessionKind {
	switch strings.ToLower(f.SessionType) {
	case "wayland":
		return contracts.SessionWayland
	case "x11":
		return contracts.SessionX11
	}
	// XDG_SESSION_TYPE is unset in some login setups ("tty" included).
	switch {
	case f.WaylandDisplay != "":
		return contracts.SessionWayland
	case f.X11Display != "":
		return contracts.SessionX11
	}
	return ""
}

func compositorName(f linuxFacts) string {
	switch {
	case f.Niri:
		return "niri"
	case f.Hyprland:
		return "hyprland"
	case f.Sway:
		return "sway"
	}
	d := strings.ToLower(f.Desktop)
	switch {
	case strings.Contains(d, "gnome"):
		return "mutter"
	case strings.Contains(d, "kde"):
		return "kwin"
	}
	return ""
}

func desktopName(f linuxFacts) string {
	if f.Desktop != "" {
		// XDG_CURRENT_DESKTOP is a colon-separated list, most specific first.
		return strings.SplitN(f.Desktop, ":", 2)[0]
	}
	return compositorName(f)
}

func portalFact(facts map[string]string, key string, p portalProbe) {
	if p.Present {
		facts[key] = strconv.FormatUint(uint64(p.Version), 10)
		return
	}
	facts[key] = "absent"
	if p.Err != "" {
		facts[key+".error"] = p.Err
	}
}

func busFact(b busName) string {
	switch {
	case b.Owned:
		return "owned"
	case b.Activatable:
		return "activatable"
	case b.Err != "":
		return "error: " + b.Err
	}
	return "absent"
}

func boolFact(b bool) string { return strconv.FormatBool(b) }

// mapLinux turns raw facts into the capability report (PRD capability matrix).
func mapLinux(f linuxFacts) contracts.CapabilityReport {
	sess := sessionKind(f)
	r := contracts.CapabilityReport{
		OS:           "linux",
		OSVersion:    f.OSVersion,
		Session:      sess,
		Desktop:      desktopName(f),
		Compositor:   compositorName(f),
		Facts:        map[string]string{},
		Capabilities: map[contracts.Capability]contracts.CapabilityState{},
	}

	r.Facts["session.type"] = f.SessionType
	r.Facts["session.wayland_display"] = f.WaylandDisplay
	r.Facts["session.x11_display"] = f.X11Display
	r.Facts["session.desktop"] = f.Desktop
	if f.PortalBusErr != "" {
		r.Facts["dbus.session.error"] = f.PortalBusErr
	}
	for iface, p := range f.Portals {
		portalFact(r.Facts, "portal."+iface[strings.LastIndex(iface, ".")+1:]+".version", p)
	}
	if p := f.Portals[portalRemoteDesktop]; p.DevicesKnown {
		r.Facts["portal.RemoteDesktop.devices"] = strconv.FormatUint(uint64(p.Devices), 10)
	}
	r.Facts["secret_service"] = busFact(f.SecretService)
	r.Facts["atspi"] = busFact(f.ATSPI)
	r.Facts["pipewire.socket"] = boolFact(f.PipeWire)
	if f.WaylandChecked {
		if f.WaylandErr != "" {
			r.Facts["wayland.error"] = f.WaylandErr
		} else {
			r.Facts["wayland.globals"] = strconv.Itoa(len(f.Globals))
			r.Facts["wayland.layer_shell"] = boolFact(f.hasGlobal(globLayerShell))
			r.Facts["wayland.ext_foreign_toplevel_list"] = boolFact(f.hasGlobal(globExtToplevelList))
			r.Facts["wayland.wlr_foreign_toplevel_manager"] = boolFact(f.hasGlobal(globWlrToplevelMgr))
			r.Facts["wayland.screencopy"] = boolFact(f.hasGlobal(globScreencopy))
			r.Facts["wayland.virtual_keyboard"] = boolFact(f.hasGlobal(globVirtualKeyboard))
			names := make([]string, 0, len(f.Globals))
			for _, g := range f.Globals {
				names = append(names, g.Interface+"@"+strconv.FormatUint(uint64(g.Version), 10))
			}
			sort.Strings(names)
			r.Facts["wayland.interfaces"] = strings.Join(names, ",")
		}
	}

	set := func(c contracts.Capability, s contracts.CapabilityState) { r.Capabilities[c] = s }
	avail := func(backend string) contracts.CapabilityState {
		return contracts.CapabilityState{Status: contracts.StatusAvailable, Backend: backend}
	}
	unavail := func(reason string, fb contracts.Capability) contracts.CapabilityState {
		return contracts.CapabilityState{Status: contracts.StatusUnavailable, Reason: reason, Fallback: fb}
	}
	unknown := func(reason string) contracts.CapabilityState {
		return contracts.CapabilityState{Status: contracts.StatusUnknown, Reason: reason}
	}
	portal := func(iface string) (portalProbe, string) {
		p := f.Portals[iface]
		if p.Present {
			return p, ""
		}
		reason := "portal " + iface[strings.LastIndex(iface, ".")+1:] + " absent"
		if f.PortalBusErr != "" {
			reason = "session bus unreachable"
		}
		return p, reason
	}

	switch sess {
	case contracts.SessionX11:
		set(contracts.CapGlobalHotkey, avail(BackendX11Grab))
		set(contracts.CapHotkeyRelease, avail(BackendX11Grab))
		set(contracts.CapScreenCapture, avail("x11-getimage"))
		set(contracts.CapOverlayClickThrough, avail("x11-shape"))
		set(contracts.CapInputInjection, avail("x11-xtest"))
		set(contracts.CapTextInsertion, avail("x11-xtest"))

	case contracts.SessionWayland:
		p, why := portal(portalGlobalShortcuts)
		switch comp := compositorName(f); {
		case comp == "niri":
			// The portal is exported, but its GNOME backend refuses
			// BindShortcuts on niri (OC-6), and niri binds fire on press
			// only.
			set(contracts.CapGlobalHotkey, ipcHotkey("GlobalShortcuts portal has no working backend on niri"))
			set(contracts.CapHotkeyRelease, contracts.CapabilityState{
				Status: contracts.StatusUnavailable, Backend: BackendIPC,
				Reason: "niri binds fire on key press only", Fallback: contracts.FallbackToggleTalk,
			})
		case p.Present:
			set(contracts.CapGlobalHotkey, avail(BackendPortal))
			// Hold-to-talk needs the Deactivated signal; backends differ and
			// the version number does not tell, so do not claim it.
			s := unknown("Deactivated delivery depends on portal backend")
			s.Backend = BackendPortal
			s.Fallback = contracts.FallbackToggleTalk
			set(contracts.CapHotkeyRelease, s)
		default:
			// Sway and others: release binds exist on some compositors
			// (bindsym --release), so release is known only once one fires.
			set(contracts.CapGlobalHotkey, ipcHotkey(why))
			s := unknown("depends on compositor release bindings")
			s.Backend = BackendIPC
			s.Fallback = contracts.FallbackToggleTalk
			set(contracts.CapHotkeyRelease, s)
		}

		// Order matches internal/platform/capture.Candidates: screencopy
		// needs no consent, the Screenshot portal works everywhere else.
		// ScreenCast+PipeWire is not a backend yet (DMA-BUF import, OC-6).
		_, whyShot := portal(portalScreenshot)
		switch {
		case f.hasGlobal(globScreencopy):
			set(contracts.CapScreenCapture, avail("wlr-screencopy"))
		case f.Portals[portalScreenshot].Present:
			s := avail("xdg-portal:Screenshot")
			s.Reason = "whole desktop per capture; permission may be requested once"
			set(contracts.CapScreenCapture, s)
		case f.Portals[portalScreenCast].Present:
			set(contracts.CapScreenCapture, unavail("ScreenCast+PipeWire capture not implemented", ""))
		default:
			set(contracts.CapScreenCapture, unavail(whyShot, ""))
		}

		switch {
		case f.WaylandErr != "":
			set(contracts.CapOverlayClickThrough, unknown("wayland registry probe failed: "+f.WaylandErr))
		case !f.WaylandChecked:
			set(contracts.CapOverlayClickThrough, unknown("wayland registry not probed"))
		case f.hasGlobal(globLayerShell):
			set(contracts.CapOverlayClickThrough, avail("wlr-layer-shell"))
		default:
			set(contracts.CapOverlayClickThrough, unavail(globLayerShell+" not advertised", contracts.CapOverlayFallbackPanel))
		}

		rd, whyRD := portal(portalRemoteDesktop)
		if rd.Present {
			s := avail("xdg-portal:RemoteDesktop")
			s.Reason = "user consent is requested on first use"
			set(contracts.CapInputInjection, s)
			s.Backend = "xdg-portal:RemoteDesktop"
			set(contracts.CapTextInsertion, s)
		} else {
			set(contracts.CapInputInjection, unavail(whyRD, ""))
			set(contracts.CapTextInsertion, unavail(whyRD, ""))
		}
		// A portal whose backend offers no keyboard cannot type (niri's
		// GNOME backend, OC-6); the compositor's virtual keyboard can.
		if rd.Present && rd.DevicesKnown && rd.Devices&1 == 0 {
			set(contracts.CapTextInsertion, unavail("RemoteDesktop portal offers no keyboard", ""))
		}
		if st := r.Capabilities[contracts.CapTextInsertion]; st.Status != contracts.StatusAvailable && f.hasGlobal(globVirtualKeyboard) {
			set(contracts.CapTextInsertion, avail(BackendVirtualKeyboard))
		}

	default:
		why := "no display session detected"
		for _, c := range []contracts.Capability{
			contracts.CapGlobalHotkey, contracts.CapHotkeyRelease, contracts.CapScreenCapture,
			contracts.CapOverlayClickThrough, contracts.CapInputInjection, contracts.CapTextInsertion,
		} {
			set(c, unavail(why, ""))
		}
	}

	// The fallback panel is an ordinary toolkit window and works wherever a
	// display session exists.
	if sess != "" {
		set(contracts.CapOverlayFallbackPanel, avail("gio-window"))
	} else {
		set(contracts.CapOverlayFallbackPanel, unavail("no display session detected", ""))
	}

	switch {
	case f.ATSPI.usable():
		set(contracts.CapAccessibility, avail("at-spi2"))
	case f.PortalBusErr != "":
		set(contracts.CapAccessibility, unknown("session bus unreachable"))
	default:
		set(contracts.CapAccessibility, unavail("org.a11y.Bus not on session bus", ""))
	}

	switch {
	case f.SecretService.usable():
		set(contracts.CapSecretStore, avail("secret-service"))
	case f.PortalBusErr != "":
		set(contracts.CapSecretStore, unknown("session bus unreachable"))
	default:
		set(contracts.CapSecretStore, unavail("org.freedesktop.secrets has no owner", ""))
	}

	if f.PipeWire {
		set(contracts.CapMicrophone, avail("pipewire"))
	} else {
		set(contracts.CapMicrophone, unknown("pipewire socket missing; ALSA/Pulse not probed"))
	}
	set(contracts.CapTray, unknown("probe not implemented"))

	return r
}
