//go:build linux

package detect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/xlsft/openclick/pkg/contracts"
)

type linuxDetector struct{}

func newDetector() contracts.CapabilityDetector { return linuxDetector{} }

func (linuxDetector) Detect(ctx context.Context) (contracts.CapabilityReport, error) {
	return mapLinux(gatherLinux(ctx)), ctx.Err()
}

func gatherLinux(ctx context.Context) linuxFacts {
	f := linuxFacts{
		OSVersion:      linuxVersion(),
		SessionType:    os.Getenv("XDG_SESSION_TYPE"),
		WaylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
		X11Display:     os.Getenv("DISPLAY"),
		Desktop:        os.Getenv("XDG_CURRENT_DESKTOP"),
		Niri:           os.Getenv("NIRI_SOCKET") != "",
		Hyprland:       os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "",
		Sway:           os.Getenv("SWAYSOCK") != "",
		Portals:        map[string]portalProbe{},
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	f.PipeWire = socketExists(filepath.Join(runtimeDir, "pipewire-0"))

	var wg sync.WaitGroup
	if f.WaylandDisplay != "" {
		f.WaylandChecked = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := listGlobals(ctx, waylandSocketPath(f.WaylandDisplay, runtimeDir))
			f.Globals = g
			if err != nil {
				f.WaylandErr = "registry probe failed"
			}
		}()
	}

	busCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(busCtx))
	if err != nil {
		f.PortalBusErr = "connect failed"
		wg.Wait()
		return f
	}
	defer conn.Close()

	var mu sync.Mutex
	for _, iface := range []string{portalGlobalShortcuts, portalScreenCast, portalRemoteDesktop} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := probePortal(busCtx, conn, iface)
			mu.Lock()
			f.Portals[iface] = p
			mu.Unlock()
		}()
	}
	wg.Add(2)
	go func() { defer wg.Done(); f.SecretService = probeName(busCtx, conn, "org.freedesktop.secrets") }()
	go func() { defer wg.Done(); f.ATSPI = probeName(busCtx, conn, "org.a11y.Bus") }()
	wg.Wait()
	return f
}

// probePortal reads only the read-only "version" property; no portal method
// is called, so no consent dialog can appear.
func probePortal(ctx context.Context, conn *dbus.Conn, iface string) portalProbe {
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var v dbus.Variant
	err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, "version").Store(&v)
	if err != nil {
		return portalProbe{Err: dbusReason(err)}
	}
	n, ok := v.Value().(uint32)
	if !ok {
		return portalProbe{Err: "unexpected version type"}
	}
	return portalProbe{Present: true, Version: n}
}

func probeName(ctx context.Context, conn *dbus.Conn, name string) busName {
	var b busName
	var owned bool
	bus := conn.BusObject()
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, name).Store(&owned); err != nil {
		b.Err = dbusReason(err)
		return b
	}
	b.Owned = owned
	if owned {
		return b
	}
	var act []string
	if err := bus.CallWithContext(ctx, "org.freedesktop.DBus.ListActivatableNames", 0).Store(&act); err == nil {
		for _, n := range act {
			if n == name {
				b.Activatable = true
				break
			}
		}
	}
	return b
}

// dbusReason keeps only the D-Bus error name (a technical code), never the
// free-form message.
func dbusReason(err error) string {
	var de dbus.Error
	if errors.As(err, &de) && de.Name != "" {
		return de.Name
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "call failed"
}

func socketExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

func linuxVersion() string {
	name := ""
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name = strings.Trim(v, `"`)
			}
		}
	}
	kernel := ""
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		kernel = strings.TrimSpace(string(b))
	}
	switch {
	case name != "" && kernel != "":
		return name + " (kernel " + kernel + ")"
	case name != "":
		return name
	}
	return kernel
}
