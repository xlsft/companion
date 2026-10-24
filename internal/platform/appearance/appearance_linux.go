//go:build linux

package appearance

import (
	"context"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/xlsft/companion/pkg/contracts"
)

const (
	portalName  = "org.freedesktop.portal.Desktop"
	portalPath  = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	settingsIf  = "org.freedesktop.portal.Settings"
	appearNS    = "org.freedesktop.appearance"
	colorScheme = "color-scheme"
	gsIface     = "org.gnome.desktop.interface"
)

// New returns the xdg-desktop-portal backed source. The portal lives on the
// session bus, so it works on X11 sessions too.
func New() contracts.AppearanceSource { return &linuxSource{state: newState()} }

type linuxSource struct{ *state }

// Start reads the scheme and subscribes to Settings.SettingChanged. Without
// a portal it falls back to polling gsettings, which has no signal we can
// use without another dependency.
func (l *linuxSource) Start(ctx context.Context) error {
	if bus, err := dbus.ConnectSessionBus(); err == nil {
		if _, ok := readPortal(ctx, bus); ok {
			sigs := make(chan *dbus.Signal, 8)
			bus.Signal(sigs)
			err := bus.AddMatchSignal(
				dbus.WithMatchObjectPath(portalPath),
				dbus.WithMatchInterface(settingsIf),
				dbus.WithMatchMember("SettingChanged"),
			)
			if err == nil {
				l.set(read(ctx, bus))
				go l.watch(ctx, bus, sigs)
				return nil
			}
		}
		_ = bus.Close()
	}
	if _, err := exec.LookPath("gsettings"); err != nil {
		return contracts.Unsupported(contracts.CapColorScheme)
	}
	l.set(read(ctx, nil))
	go l.poll(ctx, pollEvery, func(ctx context.Context) (bool, error) { return read(ctx, nil), nil })
	return nil
}

func (l *linuxSource) watch(ctx context.Context, bus *dbus.Conn, sigs chan *dbus.Signal) {
	defer bus.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case sg, ok := <-sigs:
			if !ok {
				return
			}
			if !isSchemeChange(sg) {
				continue
			}
			// Re-read instead of trusting the payload: a "no preference"
			// value needs the gsettings fallback anyway.
			l.set(read(ctx, bus))
		}
	}
}

func isSchemeChange(sg *dbus.Signal) bool {
	if sg.Name != settingsIf+".SettingChanged" || len(sg.Body) < 2 {
		return false
	}
	ns, _ := sg.Body[0].(string)
	key, _ := sg.Body[1].(string)
	return ns == appearNS && key == colorScheme
}

// read combines the portal (when bus is set) with the GNOME fallback.
func read(ctx context.Context, bus *dbus.Conn) bool {
	var v uint32
	var have bool
	if bus != nil {
		v, have = readPortal(ctx, bus)
	}
	if have && (v == 1 || v == 2) {
		return v == 1
	}
	return decide(v, have, gsettingsGet(ctx, "color-scheme"), gsettingsGet(ctx, "gtk-theme"))
}

func readPortal(ctx context.Context, bus *dbus.Conn) (uint32, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var v dbus.Variant
	err := bus.Object(portalName, portalPath).CallWithContext(ctx, settingsIf+".ReadOne", 0, appearNS, colorScheme).Store(&v)
	if err != nil {
		return 0, false
	}
	return schemeValue(v)
}

// schemeValue unwraps the (possibly nested) variant holding the uint32.
func schemeValue(v dbus.Variant) (uint32, bool) {
	for {
		switch x := v.Value().(type) {
		case dbus.Variant:
			v = x
		case uint32:
			return x, true
		default:
			return 0, false
		}
	}
}

// gsettingsGet returns the raw value ('prefer-dark', 'Adwaita-dark') or "".
func gsettingsGet(ctx context.Context, key string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "get", gsIface, key).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
