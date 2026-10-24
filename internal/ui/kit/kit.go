// Package kit holds the small widgets shared by the shell windows and the
// setup wizard: labels, buttons with a visible focus ring, cards and the
// capability report rows of the Diagnostics view.
package kit

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/theme"
)

// FocusRing draws a 2dp accent outline around w when focused, so keyboard
// focus is visible beyond material's faint hover tint (FR-UI-007).
func FocusRing(gtx layout.Context, th *theme.Theme, focused bool, w layout.Widget) layout.Dimensions {
	if !focused {
		return w(gtx)
	}
	return widget.Border{Color: th.Pal.Accent, Width: 2, CornerRadius: 4}.Layout(gtx, w)
}

// Button is a material button with a visible focus ring.
func Button(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	return FocusRing(gtx, th, gtx.Focused(b), material.Button(th.Theme, b, label).Layout)
}

func Heading(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(1.6), txt)
	l.Font.Weight = font.SemiBold
	return l
}

func Subheading(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(1.15), txt)
	l.Font.Weight = font.SemiBold
	return l
}

func Body(th *theme.Theme, txt string) material.LabelStyle {
	return material.Label(th.Theme, th.Sp(1), txt)
}

func Muted(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(0.9), txt)
	l.Color = th.Pal.Muted
	return l
}

func VSpace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

// Card draws w on a rounded surface.
func Card(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			rr := gtx.Dp(8)
			defer clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, rr).Push(gtx.Ops).Pop()
			paint.Fill(gtx.Ops, th.Pal.Surface)
			return layout.Dimensions{Size: gtx.Constraints.Min}
		},
		func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(16).Layout(gtx, w)
		})
}

// OrDash shows an em dash for an empty value.
func OrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
