// Package theme is the Companion look on top of Gio's material widgets:
// light/dark palettes, a user text scale and status styling that never
// relies on colour alone (NFR-005).
package theme

import (
	"image/color"

	"gioui.org/font/gofont"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// BaseTextSize is the body text size at scale 1.
const BaseTextSize unit.Sp = 16

// Scale bounds; the step is what Ctrl+Plus / Ctrl+Minus apply.
const (
	MinScale  float32 = 0.75
	MaxScale  float32 = 2.0
	ScaleStep float32 = 0.125
)

// Palette holds every colour the shell draws with.
type Palette struct {
	Bg, Surface, Sidebar, Fg, Muted, Divider color.NRGBA
	Accent, AccentFg, Selected, Scrim        color.NRGBA
	OK, Warn, Error                          color.NRGBA
}

// Light has WCAG AA contrast for Fg/Muted on Bg/Surface.
var Light = Palette{
	Bg:       rgb(0xf6f7f9),
	Surface:  rgb(0xffffff),
	Sidebar:  rgb(0xeceef2),
	Fg:       rgb(0x1b1d22),
	Muted:    rgb(0x575c66),
	Divider:  rgb(0xd5d8de),
	Accent:   rgb(0x2f4fc9),
	AccentFg: rgb(0xffffff),
	Selected: rgb(0xd9e0fa),
	Scrim:    color.NRGBA{A: 0x88},
	OK:       rgb(0x1d7a3a),
	Warn:     rgb(0x8a5a00),
	Error:    rgb(0xb3261e),
}

var Dark = Palette{
	Bg:       rgb(0x16181c),
	Surface:  rgb(0x1f2228),
	Sidebar:  rgb(0x1b1d22),
	Fg:       rgb(0xe8eaee),
	Muted:    rgb(0xa5abb6),
	Divider:  rgb(0x353a43),
	Accent:   rgb(0x8ea6ff),
	AccentFg: rgb(0x0d1330),
	Selected: rgb(0x2c3557),
	Scrim:    color.NRGBA{A: 0xaa},
	OK:       rgb(0x6fd08c),
	Warn:     rgb(0xf0c062),
	Error:    rgb(0xff8a80),
}

// Theme is per window: Gio text shapers must not be shared between windows.
type Theme struct {
	*material.Theme
	Pal   Palette
	Dark  bool
	Scale float32
}

// New returns a theme with its own shaper and the bundled Go fonts, which
// cover Latin and Cyrillic for the ru/en UI.
func New(dark bool, scale float32) *Theme {
	m := material.NewTheme()
	m.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	t := &Theme{Theme: m}
	t.Apply(dark, scale)
	return t
}

// Apply switches palette and text scale in place; cheap enough per frame.
func (t *Theme) Apply(dark bool, scale float32) {
	t.Dark = dark
	t.Scale = ClampScale(scale)
	t.Pal = Light
	if dark {
		t.Pal = Dark
	}
	t.Palette = material.Palette{Bg: t.Pal.Bg, Fg: t.Pal.Fg, ContrastBg: t.Pal.Accent, ContrastFg: t.Pal.AccentFg}
	t.TextSize = t.Sp(1)
}

// Sp scales a size relative to the body text size, e.g. Sp(1.5) for a
// heading, so all text follows the user's scale.
func (t *Theme) Sp(rel float32) unit.Sp { return unit.Sp(float32(BaseTextSize) * rel * t.Scale) }

// ClampScale keeps a text scale within [MinScale, MaxScale]; 0 means 1.
func ClampScale(s float32) float32 {
	switch {
	case s == 0:
		return 1
	case s < MinScale:
		return MinScale
	case s > MaxScale:
		return MaxScale
	}
	return s
}

// SelectedMarker prefixes the selected navigation entry so selection is not
// shown by colour alone.
const SelectedMarker = "►"

// Status is a semantic state shown with a symbol, a word and a colour.
type Status int

const (
	StatusUnknown Status = iota
	StatusOK
	StatusWarn
	StatusError
)

// Symbol is the non-colour cue for s.
func (s Status) Symbol() string {
	switch s {
	case StatusOK:
		return "√"
	case StatusWarn:
		return "▲"
	case StatusError:
		return "×"
	}
	return "?"
}

// Color for s in palette p.
func (p Palette) Color(s Status) color.NRGBA {
	switch s {
	case StatusOK:
		return p.OK
	case StatusWarn:
		return p.Warn
	case StatusError:
		return p.Error
	}
	return p.Muted
}

// StatusLabel renders "symbol text" in the status colour.
func (t *Theme) StatusLabel(s Status, txt string) material.LabelStyle {
	l := material.Label(t.Theme, t.Sp(1), s.Symbol()+" "+txt)
	l.Color = t.Pal.Color(s)
	return l
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}
