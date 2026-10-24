package kit

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Window chrome: page headers, navigation entries and the chat's round
// buttons, avatar and bubble.

// PageHeader is the 54px bar on top of a page: a title on the left,
// optional actions on the right and an edge-to-edge rule below.
func PageHeader(gtx layout.Context, th *theme.Theme, title layout.Widget, actions layout.Widget) layout.Dimensions {
	h := gtx.Dp(th.Dp(54))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
			return layout.Inset{Left: th.Dp(28), Right: th.Dp(28)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						d := VCenter(gtx, gtx.Constraints.Max.Y, title)
						d.Size.X = gtx.Constraints.Max.X
						return d
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if actions == nil {
							return layout.Dimensions{}
						}
						return VCenter(gtx, gtx.Constraints.Max.Y, actions)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Divider(gtx, th) }),
	)
}

// PageTitle is the medium-weight title inside a PageHeader.
func PageTitle(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 14, txt, th.Pal.Fg)
	l.Font.Weight = font.Medium
	l.MaxLines = 1
	return l
}

// Breadcrumb is a PageHeader title of a section inside a page:
// "Settings / Voice".
func Breadcrumb(th *theme.Theme, parent, cur string) layout.Widget {
	p := label(th, 14, parent, th.Pal.Muted)
	p.MaxLines = 1
	sep := label(th, 14, "/", th.Pal.Subtle)
	return HStack(th, 8, p.Layout, sep.Layout, PageTitle(th, cur).Layout)
}

// NavItem is a sidebar entry. The selected one is tinted, drawn in the
// primary text colour and medium weight and carries an accent bar on its
// leading edge, so selection never depends on colour alone (NFR-005).
func NavItem(gtx layout.Context, th *theme.Theme, c *widget.Clickable, n icon.Name, txt string, selected bool) layout.Dimensions {
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.SelectedOp(selected).Add(gtx.Ops)
			bg := color.NRGBA{}
			switch {
			case selected:
				bg = th.Pal.AccentSoft
			case c.Hovered():
				bg = th.Pal.Hover
			}
			fg := th.Pal.Fg2
			if selected {
				fg = th.Pal.Fg
			}
			return navRow(gtx, th, bg, selected, n, txt, fg, selected, nil)
		})
	})
}

// NavItemSoon is a sidebar entry for a section that is not available yet:
// dimmed, with a tag, and not focusable or clickable at all.
func NavItemSoon(gtx layout.Context, th *theme.Theme, n icon.Name, txt, soon string) layout.Dimensions {
	semantic.DescriptionOp(txt + ", " + soon).Add(gtx.Ops)
	return navRow(gtx, th, color.NRGBA{}, false, n, txt, th.Pal.Subtle, false, func(gtx layout.Context) layout.Dimensions {
		return Tag(gtx, th, soon, TagNeutral)
	})
}

func navRow(gtx layout.Context, th *theme.Theme, bg color.NRGBA, bar bool, n icon.Name, txt string, fg color.NRGBA, bold bool, trail layout.Widget) layout.Dimensions {
	h := gtx.Dp(th.Dp(38))
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	if bg.A != 0 {
		rrect(gtx, gtx.Dp(th.Dp(4)), bg)
	}
	if bar {
		b := image.Rect(0, h/4, max(2, gtx.Dp(th.Dp(2))), h-h/4)
		paint.FillShape(gtx.Ops, th.Pal.AccentLine, clip.UniformRRect(b, b.Dx()/2).Op(gtx.Ops))
	}
	return layout.Inset{Left: th.Dp(14), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return VCenter(gtx, h, IconW(th, n, 16, fg)) }),
			HSpace(th.Dp(10)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				l := label(th, 14, txt, fg)
				l.MaxLines = 1
				if bold {
					l.Font.Weight = font.Medium
				}
				return VCenter(gtx, h, l.Layout)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if trail == nil {
					return layout.Dimensions{}
				}
				return VCenter(gtx, h, trail)
			}),
		)
	})
}

// RoundButtonKind picks a RoundIconButton's look.
type RoundButtonKind int

const (
	// RoundPrimary is the violet send button.
	RoundPrimary RoundButtonKind = iota
	// RoundSecondary is outlined on a hover fill, like the mic button.
	RoundSecondary
	// RoundActive is a pressed toggle: accent tint and outline.
	RoundActive
)

// RoundIconButton is the composer's 36px circular button; desc is its
// accessible name.
func RoundIconButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, n icon.Name, desc string, kind RoundButtonKind) layout.Dimensions {
	disabled := !gtx.Enabled()
	bg, fg, border := th.Pal.Accent, th.Pal.AccentFg, color.NRGBA{}
	switch kind {
	case RoundSecondary:
		bg, fg, border = th.Pal.Hover, th.Pal.Fg, th.Pal.Border
	case RoundActive:
		bg, fg, border = th.Pal.AccentSoft, th.Pal.AccentText, th.Pal.AccentLine
	}
	if !disabled && c.Hovered() {
		switch kind {
		case RoundPrimary:
			bg = th.Pal.AccentLine
		case RoundSecondary:
			bg = th.Pal.Border
		}
	}
	if disabled {
		if kind == RoundPrimary {
			bg, fg = th.Pal.AccentDisabled, th.Pal.FgDisabled
		} else {
			bg, fg, border = color.NRGBA{}, th.Pal.Subtle, th.Pal.Divider
		}
	}
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			return Disc(th, 36, bg, border, IconW(th, n, 16, fg))(gtx)
		})
	})
}

// Avatar is the assistant's 28px mark beside its replies; active tints it
// while the reply is still arriving.
func Avatar(gtx layout.Context, th *theme.Theme, active bool) layout.Dimensions {
	s := gtx.Dp(th.Dp(28))
	gtx.Constraints = layout.Exact(image.Pt(s, s))
	bg, fg := th.Pal.Raised, th.Pal.AccentLine
	if active {
		bg, fg = th.Pal.AccentSoft, th.Pal.AccentText
	}
	rrect(gtx, gtx.Dp(th.Dp(6)), bg)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := label(th, 15, "↖", fg)
		l.Font = font.Font{Typeface: th.Display(), Weight: font.Bold}
		return l.Layout(gtx)
	})
}

// Bubble draws w on a rounded fill whose bottom-right corner is nearly
// square, pointing at the sender like the prototypes' user messages.
func Bubble(gtx layout.Context, th *theme.Theme, col color.NRGBA, w layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			r, tail := gtx.Dp(th.Dp(10)), gtx.Dp(th.Dp(2))
			rect := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, col, clip.RRect{Rect: rect, NW: r, NE: r, SE: tail, SW: r}.Op(gtx.Ops))
			return layout.Dimensions{Size: gtx.Constraints.Min}
		},
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(10), Bottom: th.Dp(10), Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx, w)
		})
}
