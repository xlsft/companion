//go:build linux

package appearance

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestSchemeValueUnwrapsNestedVariants(t *testing.T) {
	tests := []struct {
		name string
		in   dbus.Variant
		want uint32
		ok   bool
	}{
		{"plain", dbus.MakeVariant(uint32(1)), 1, true},
		{"nested", dbus.MakeVariant(dbus.MakeVariant(uint32(2))), 2, true},
		{"wrong type", dbus.MakeVariant("dark"), 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := schemeValue(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("schemeValue = %d, %v", got, ok)
			}
		})
	}
}

func TestIsSchemeChange(t *testing.T) {
	sig := func(name string, body ...any) *dbus.Signal { return &dbus.Signal{Name: name, Body: body} }
	tests := []struct {
		name string
		sg   *dbus.Signal
		want bool
	}{
		{"color-scheme", sig(settingsIf+".SettingChanged", appearNS, colorScheme, dbus.MakeVariant(uint32(1))), true},
		{"other key", sig(settingsIf+".SettingChanged", appearNS, "accent-color", dbus.MakeVariant(uint32(1))), false},
		{"other namespace", sig(settingsIf+".SettingChanged", "org.gnome.desktop.interface", colorScheme), false},
		{"other signal", sig("org.x.Other", appearNS, colorScheme), false},
		{"short body", sig(settingsIf+".SettingChanged", appearNS), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSchemeChange(tc.sg); got != tc.want {
				t.Errorf("isSchemeChange = %v, want %v", got, tc.want)
			}
		})
	}
}
