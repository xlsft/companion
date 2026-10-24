package theme

import (
	"math"
	"testing"
)

func TestClampScale(t *testing.T) {
	tests := []struct{ in, want float32 }{
		{0, 1}, {0.5, MinScale}, {1.25, 1.25}, {3, MaxScale},
	}
	for _, tt := range tests {
		if got := ClampScale(tt.in); got != tt.want {
			t.Errorf("ClampScale(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestApplyScalesText(t *testing.T) {
	th := New(false, 1.5)
	if th.TextSize != 24 || th.Sp(2) != 48 {
		t.Errorf("TextSize=%v Sp(2)=%v", th.TextSize, th.Sp(2))
	}
	th.Apply(true, 1)
	if th.Bg != Dark.Bg || th.TextSize != BaseTextSize {
		t.Errorf("dark apply: bg=%v size=%v", th.Bg, th.TextSize)
	}
}

func TestStatusSymbolsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range []Status{StatusUnknown, StatusOK, StatusWarn, StatusError} {
		if seen[s.Symbol()] {
			t.Errorf("duplicate symbol %q", s.Symbol())
		}
		seen[s.Symbol()] = true
	}
}

// Text must stay readable in both palettes (WCAG AA 4.5:1 for body text).
func TestPaletteContrast(t *testing.T) {
	for name, p := range map[string]Palette{"light": Light, "dark": Dark} {
		for _, pair := range []struct {
			label  string
			fg, bg [3]uint8
		}{
			{"fg/bg", rgb3(p.Fg), rgb3(p.Bg)},
			{"muted/surface", rgb3(p.Muted), rgb3(p.Surface)},
			{"accentfg/accent", rgb3(p.AccentFg), rgb3(p.Accent)},
			{"ok/surface", rgb3(p.OK), rgb3(p.Surface)},
			{"warn/surface", rgb3(p.Warn), rgb3(p.Surface)},
			{"error/surface", rgb3(p.Error), rgb3(p.Surface)},
			{"fg/selected", rgb3(p.Fg), rgb3(p.Selected)},
		} {
			if r := contrast(pair.fg, pair.bg); r < 4.5 {
				t.Errorf("%s %s contrast %.2f < 4.5", name, pair.label, r)
			}
		}
	}
}

func rgb3(c interface{ RGBA() (r, g, b, a uint32) }) [3]uint8 {
	r, g, b, _ := c.RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

func luminance(c [3]uint8) float64 {
	ch := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c[0]) + 0.7152*ch(c[1]) + 0.0722*ch(c[2])
}

func contrast(a, b [3]uint8) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
