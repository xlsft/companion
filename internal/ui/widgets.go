package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/openclick/internal/ui/theme"
)

// focusRing draws a 2dp accent outline around w when focused, so keyboard
// focus is visible beyond material's faint hover tint (FR-UI-007).
func focusRing(gtx layout.Context, th *theme.Theme, focused bool, w layout.Widget) layout.Dimensions {
	if !focused {
		return w(gtx)
	}
	return widget.Border{Color: th.Pal.Accent, Width: 2, CornerRadius: 4}.Layout(gtx, w)
}

// button is a material button with a visible focus ring.
func button(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	return focusRing(gtx, th, gtx.Focused(b), material.Button(th.Theme, b, label).Layout)
}

func heading(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(1.6), txt)
	l.Font.Weight = font.SemiBold
	return l
}

func subheading(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(1.15), txt)
	l.Font.Weight = font.SemiBold
	return l
}

func body(th *theme.Theme, txt string) material.LabelStyle {
	return material.Label(th.Theme, th.Sp(1), txt)
}

func muted(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(0.9), txt)
	l.Color = th.Pal.Muted
	return l
}

func vspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

// card draws w on a rounded surface.
func card(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
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

// placeholderPage stands in for sections that are not implemented yet.
type placeholderPage struct{ route Route }

func (p placeholderPage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading(th, T(l, p.route.labelKey())).Layout),
			vspace(12),
			layout.Rigid(muted(th, T(l, "placeholder.body")).Layout),
		)
	})
}

// dialog is a modal message box; Esc or its button closes it.
type dialog struct {
	open      bool
	needFocus bool
	title     string
	text      string
	closeBtn  widget.Clickable
}

func (d *dialog) show(title, text string) {
	d.open, d.needFocus, d.title, d.text = true, true, title, text
}

func (d *dialog) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if !d.open {
		return layout.Dimensions{}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			d.open = false
		}
	}
	if d.closeBtn.Clicked(gtx) {
		d.open = false
	}
	if !d.open {
		gtx.Execute(op.InvalidateCmd{})
		return layout.Dimensions{}
	}
	size := gtx.Constraints.Max
	// The scrim swallows pointer input so the page underneath is inert.
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, d)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press | pointer.Release | pointer.Scroll}); !ok {
			break
		}
	}
	paint.ColorOp{Color: th.Pal.Scrim}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	area.Pop()

	if d.needFocus {
		gtx.Execute(key.FocusCmd{Tag: &d.closeBtn})
		d.needFocus = false
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(420*th.Scale)))
		return card(gtx, th, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(subheading(th, d.title).Layout),
				vspace(8),
				layout.Rigid(body(th, d.text).Layout),
				vspace(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return button(gtx, th, &d.closeBtn, T(l, "dialog.close"))
				}),
			)
		})
	})
}
