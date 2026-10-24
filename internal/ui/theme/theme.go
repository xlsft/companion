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
	// coloured text and icons.
	Accent, AccentFg, AccentLine, AccentText color.NRGBA
	// AccentDisabled/FgDisabled draw a disabled primary button.
	AccentDisabled, FgDisabled color.NRGBA
	// Scrim dims the window under a dialog; it is the only translucent
	// token.
	Scrim           color.NRGBA
	OK, Warn, Error color.NRGBA

	// The tints below are opaque: the prototypes' rgba() tints mixed into
	// their background in sRGB, as a browser composites them. Gio blends
	// translucent fills in linear light, which makes low-alpha tints come
	// out much stronger, so nothing translucent is painted (see mix).

	// AccentSoft marks the selected navigation entry, the current step and
	// tags; AccentTint the chosen card; AccentStrong the user's message.
	AccentSoft, AccentTint, AccentStrong color.NRGBA
	// Selected is the text selection.
	Selected color.NRGBA
	// OKSoft, WarnSoft and ErrSoft are badge fills on a Surface.
	OKSoft, WarnSoft, ErrSoft color.NRGBA
	// WarnTint and ErrTint tint a whole row or block that needs the user.
	WarnTint, ErrTint color.NRGBA
}

// tints are the prototypes' rgba() bases and alphas for the opaque tint
// tokens of a palette.
type tints struct {
	accentSoft, accentTint, accentStrong float32
	// selected is the text selection: strong enough to read on a field,
	// which AccentSoft (a hover-grade tint) is not.
	selected        float32
	ok, warn, err   float32
	warnRow, errRow float32
}

// withTints fills in p's tint tokens, mixed into the backgrounds they are
// drawn on: accent tints into Bg, status tints into Surface.
func withTints(p Palette, t tints) Palette {
	p.AccentSoft = mix(p.Bg, p.AccentLine, t.accentSoft)
	p.AccentTint = mix(p.Bg, p.AccentLine, t.accentTint)
	p.AccentStrong = mix(p.Bg, p.AccentLine, t.accentStrong)
	p.Selected = mix(p.Bg, p.AccentLine, t.selected)
	p.OKSoft = mix(p.Surface, rgb(0x10b981), t.ok)
	p.WarnSoft = mix(p.Surface, rgb(0xf97316), t.warn)
	p.ErrSoft = mix(p.Surface, rgb(0xef4444), t.err)
	p.WarnTint = mix(p.Surface, rgb(0xf97316), t.warnRow)
	p.ErrTint = mix(p.Surface, rgb(0xef4444), t.errRow)
	return p
}

// mix is src at alpha a over the opaque dst, composited in sRGB as CSS
// does.
func mix(dst, src color.NRGBA, a float32) color.NRGBA {
	m := func(d, s uint8) uint8 { return uint8(float32(d)*(1-a) + float32(s)*a + 0.5) }
	return color.NRGBA{R: m(dst.R, src.R), G: m(dst.G, src.G), B: m(dst.B, src.B), A: 0xff}
}

var Dark = withTints(Palette{
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
	AccentDisabled: hsl(258, 40, 30),
	FgDisabled:     hsl(240, 10, 75),
	Scrim:          color.NRGBA{A: 0xaa},
	OK:             rgb(0x34d399),
	Warn:           rgb(0xfb923c),
	Error:          rgb(0xf87171),
}, tints{accentSoft: 0.12, accentTint: 0.08, accentStrong: 0.18, selected: 0.40, ok: 0.12, warn: 0.14, err: 0.14, warnRow: 0.05, errRow: 0.06})

var Light = withTints(Palette{
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
	AccentDisabled: hsl(258, 60, 85),
	FgDisabled:     hsl(258, 20, 45),
	Scrim:          color.NRGBA{A: 0x88},
	OK:             rgb(0x047857),
	Warn:           rgb(0xc2410c),
	Error:          rgb(0xb91c1c),
}, tints{accentSoft: 0.10, accentTint: 0.06, accentStrong: 0.16, selected: 0.38, ok: 0.12, warn: 0.12, err: 0.10, warnRow: 0.05, errRow: 0.05})

// Typefaces of the bundled fonts (see fonts/).
const (
	Sans    font.Typeface = "Google Sans"
	Mono    font.Typeface = "Google Sans Code"
	Display font.Typeface = "Unbounded"
)

// CJK typefaces, one per language: Han characters are drawn differently
// in Chinese, Japanese and Korean, so each UI language falls back to its
// own face rather than a shared one.
const (
	cjkSC font.Typeface = "Noto Sans CJK SC"
	cjkJP font.Typeface = "Noto Sans CJK JP"
	cjkKR font.Typeface = "Noto Sans CJK KR"
)

// cjkFor maps a UI language code to its CJK face. Other languages use the
// Chinese one, so a CJK name in a Latin UI (the language picker lists all
// three) still has glyphs.
func cjkFor(lang string) font.Typeface {
	switch lang {
	case "ja":
		return cjkJP
	case "ko":
		return cjkKR
	}
	return cjkSC
}

// withFallback is a typeface list: Gio treats a comma-separated Typeface
// as a family list tried in order, per rune.
func withFallback(base, fallback font.Typeface) font.Typeface { return base + ", " + fallback }

// Theme is per window: Gio text shapers must not be shared between windows.
type Theme struct {
	*material.Theme
	Pal   Palette
	Dark  bool
	Scale float32

	// Typeface lists with the CJK fallback, rebuilt only on a language change.
	lang                string
	sans, mono, display font.Typeface
}

// New returns a theme with its own shaper over the bundled fonts.
func New(dark bool, scale float32) *Theme {
	m := material.NewTheme()
	m.Shaper = text.NewShaper(text.WithCollection(Collection()))
	t := &Theme{Theme: m}
	t.SetLanguage("")
	t.Apply(dark, scale)
	return t
}

// SetLanguage picks the CJK fallback face for a UI language code; call it
// once per frame with the current language, it is cheap.
func (t *Theme) SetLanguage(lang string) {
	if t.sans != "" && t.lang == lang {
		return
	}
	cjk := cjkFor(lang)
	t.lang = lang
	t.sans, t.mono, t.display = withFallback(Sans, cjk), withFallback(Mono, cjk), withFallback(Display, cjk)
	t.Face = t.sans
}

// Sans, Mono and Display are the bundled typefaces followed by the CJK
// face of the UI language: Latin and Cyrillic stay in the Latin face and
// only runes it lacks fall to the CJK one. Use these, not the bare
// constants, for anything that shows translated or user text.
func (t *Theme) Sans() font.Typeface    { return t.sans }
func (t *Theme) Mono() font.Typeface    { return t.mono }
func (t *Theme) Display() font.Typeface { return t.display }

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
