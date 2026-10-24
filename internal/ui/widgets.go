package ui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Shared widgets live in kit so the wizard package can use them too.
var (
	button     = kit.Button
	subheading = kit.Subheading
	body       = kit.Body
	muted      = kit.Muted
	vspace     = kit.VSpace
	card       = kit.Card
)

// pageScaffold is a main-window page: the 54px header with title and
// actions, then rows in a scrolling list with the prototypes' padding.
func pageScaffold(gtx layout.Context, th *theme.Theme, title string, actions layout.Widget, list *widget.List, rows []layout.Widget) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kit.PageHeader(gtx, th, kit.PageTitle(th, title).Layout, actions)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			list.Axis = layout.Vertical
			return material.List(th.Theme, list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
				in := layout.Inset{Left: th.Dp(28), Right: th.Dp(28)}
				if i == 0 {
					in.Top = th.Dp(24)
				}
				if i == len(rows)-1 {
					in.Bottom = th.Dp(24)
				}
				return in.Layout(gtx, rows[i])
			})
		}),
	)
}

// placeholderPage stands in for sections that are not implemented yet.
type placeholderPage struct{ route Route }

func (p placeholderPage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	return pageScaffold(gtx, th, T(l, p.route.labelKey()), nil, new(widget.List), []layout.Widget{muted(th, T(l, "placeholder.body")).Layout})
}

// dialog is a modal message box; Esc or its button closes it.
type dialog struct {
	open      bool
	needFocus bool
	title     string
	text      string
	// note is an optional small print line under the text.
	note     string
	closeBtn widget.Clickable
}

func (d *dialog) show(title, text string) { d.showNote(title, text, "") }

func (d *dialog) showNote(title, text, note string) {
	d.open, d.needFocus, d.title, d.text, d.note = true, true, title, text, note
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
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(440)))
		return kit.Box(gtx, th, th.Pal.Surface, th.Pal.Border, 6, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(th.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(kit.Title(th, d.title).Layout),
					vspace(th.Dp(10)),
					layout.Rigid(kit.Lead(th, d.text).Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if d.note == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Top: th.Dp(12)}.Layout(gtx, kit.Small(th, d.note).Layout)
					}),
					vspace(th.Dp(20)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return kit.SecondaryButton(gtx, th, &d.closeBtn, T(l, "dialog.close"))
						})
					}),
				)
			})
		})
	})
}
