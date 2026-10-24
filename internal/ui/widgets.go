package ui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Shared widgets live in kit so the wizard package can use them too.
var (
	focusRing  = kit.FocusRing
	button     = kit.Button
	heading    = kit.Heading
	subheading = kit.Subheading
	body       = kit.Body
	muted      = kit.Muted
	vspace     = kit.VSpace
	card       = kit.Card
)

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
