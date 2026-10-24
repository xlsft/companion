// Package theme is the Companion look on top of Gio's material widgets:
// the design prototypes' palette and fonts, a user text scale and status
// styling that never relies on colour alone (NFR-005).
package theme

import (
	"image/color"
	"math"

	"gioui.org/font"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// BaseTextSize is the body text size at scale 1.
const BaseTextSize unit.Sp = 14

// Scale bounds; the step is what Ctrl+Plus / Ctrl+Minus apply.
const (
	MinScale  float32 = 0.75
	MaxScale  float32 = 2.0
	ScaleStep float32 = 0.125
)

// Palette holds every colour the UI draws with. The dark values are the
// design prototypes' tokens; the light ones are their counterparts tuned
// for WCAG AA on white.
type Palette struct {
	// Bg is the window, Sidebar and Surface the panels and cards on it,
	// Raised the controls and icon tiles inside them, Hover their pointer
	// state and Code the background of monospace blocks.
	Bg, Sidebar, Surface, Raised, Hover, Code color.NRGBA
	// Divider separates regions; Border outlines controls.
	Divider, Border color.NRGBA
	// Fg is primary text, Fg2 secondary body text, Muted captions and
	// Subtle the least important labels (inactive steps, timestamps).
	Fg, Fg2, Muted, Subtle color.NRGBA
	// Accent fills primary buttons under AccentFg text; AccentLine is the
	// brand violet for marks, progress and focus; AccentText is accent
	// coloured text and icons; AccentSoft tints selected items.
	Accent, AccentFg, AccentLine, AccentText, AccentSoft color.NRGBA
	// AccentDisabled/FgDisabled draw a disabled primary button.
	AccentDisabled, FgDisabled color.NRGBA
	Selected, Scrim            color.NRGBA
	OK, Warn, Error            color.NRGBA
	OKSoft, WarnSoft, ErrSoft  color.NRGBA
}

var Dark = Palette{
	Bg:             hsl(240, 5, 9),
	Sidebar:        hsl(240, 5, 9),
	Surface:        hsl(240, 5, 11),
	Raised:         hsl(240, 5, 13),
	Hover:          hsl(240, 5, 15),
	Code:           hsl(240, 5, 6),
	Divider:        hsl(240, 5, 18),
	Border:         hsl(240, 5, 22),
	Fg:             hsl(240, 15, 99),
	Fg2:            hsl(240, 5, 78),
	Muted:          hsl(240, 5, 70),
	Subtle:         hsl(240, 5, 62),
	Accent:         rgb(0x7c3aed),
	AccentFg:       rgb(0xffffff),
	AccentLine:     rgb(0x8b5cf6),
	AccentText:     rgb(0xa78bfa),
	AccentSoft:     rgba(0x8b5cf6, 0.12),
	AccentDisabled: hsl(258, 40, 30),
	FgDisabled:     hsl(240, 10, 75),
	Selected:       rgba(0x8b5cf6, 0.12),
	Scrim:          color.NRGBA{A: 0xaa},
	OK:             rgb(0x34d399),
	Warn:           rgb(0xfb923c),
	Error:          rgb(0xf87171),
	OKSoft:         rgba(0x10b981, 0.12),
	WarnSoft:       rgba(0xf97316, 0.14),
	ErrSoft:        rgba(0xef4444, 0.14),
}

var Light = Palette{
	Bg:             hsl(240, 5, 98),
	Sidebar:        hsl(240, 5, 98),
	Surface:        rgb(0xffffff),
	Raised:         hsl(240, 5, 95),
	Hover:          hsl(240, 5, 92),
	Code:           hsl(240, 5, 96),
	Divider:        hsl(240, 6, 90),
	Border:         hsl(240, 6, 84),
	Fg:             hsl(240, 10, 4),
	Fg2:            hsl(240, 5, 26),
	Muted:          hsl(240, 4, 40),
	Subtle:         hsl(240, 4, 44),
	Accent:         rgb(0x7c3aed),
	AccentFg:       rgb(0xffffff),
	AccentLine:     rgb(0x8b5cf6),
	AccentText:     rgb(0x6d28d9),
	AccentSoft:     rgba(0x8b5cf6, 0.10),
	AccentDisabled: hsl(258, 60, 85),
	FgDisabled:     hsl(258, 20, 45),
	Selected:       rgba(0x8b5cf6, 0.10),
	Scrim:          color.NRGBA{A: 0x88},
	OK:             rgb(0x047857),
	Warn:           rgb(0xc2410c),
	Error:          rgb(0xb91c1c),
	OKSoft:         rgba(0x10b981, 0.12),
	WarnSoft:       rgba(0xf97316, 0.12),
	ErrSoft:        rgba(0xef4444, 0.10),
}

// Typefaces of the bundled fonts (see fonts/).
const (
	Sans    font.Typeface = "Google Sans"
	Mono    font.Typeface = "Google Sans Code"
	Display font.Typeface = "Unbounded"
)

// Theme is per window: Gio text shapers must not be shared between windows.
type Theme struct {
	*material.Theme
	Pal   Palette
	Dark  bool
	Scale float32
}

// New returns a theme with its own shaper over the bundled fonts.
func New(dark bool, scale float32) *Theme {
	m := material.NewTheme()
	m.Shaper = text.NewShaper(text.WithCollection(Collection()))
	m.Face = Sans
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

// Dp scales a layout size with the text, so spacing keeps its proportion
// to the type at every user scale.
func (t *Theme) Dp(v float32) unit.Dp { return unit.Dp(v * t.Scale) }

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func rgba(c uint32, alpha float32) color.NRGBA {
	col := rgb(c)
	col.A = uint8(alpha*255 + 0.5)
	return col
}

// hsl takes CSS hsl() arguments (degrees, percent, percent) so tokens read
// the same as in the design prototypes.
func hsl(h, s, l float64) color.NRGBA {
	s, l = s/100, l/100
	c := (1 - math.Abs(2*l-1)) * s
	hp := h / 60
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, b float64
	switch {
	case hp < 1:
		r, g = c, x
	case hp < 2:
		r, g = x, c
	case hp < 3:
		g, b = c, x
	case hp < 4:
		g, b = x, c
	case hp < 5:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := l - c/2
	ch := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return color.NRGBA{R: ch(r), G: ch(g), B: ch(b), A: 0xff}
}
