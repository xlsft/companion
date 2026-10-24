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

// Widgets of the main window (sidebar, page headers, chat), drawn after
// the Home, Settings and Diagnostics prototypes.

// Text is a label at a prototype pixel size in col.
func Text(th *theme.Theme, size float32, txt string, col color.NRGBA) material.LabelStyle {
	return label(th, size, txt, col)
}

// Kbd shows a key chord as the prototypes' <kbd>: mono on a raised chip.
func Kbd(gtx layout.Context, th *theme.Theme, chord string) layout.Dimensions {
	return outlined(gtx, th, th.Pal.Raised, th.Pal.Border, 4, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: th.Dp(2), Bottom: th.Dp(2), Left: th.Dp(6), Right: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := label(th, 11.5, chord, th.Pal.Fg)
			l.Font.Typeface = theme.Mono
			l.MaxLines = 1
			return l.Layout(gtx)
		})
	})
}

// Chip is a 28px outlined label with a leading icon, e.g. the model name
// in a page header.
func Chip(gtx layout.Context, th *theme.Theme, n icon.Name, txt string) layout.Dimensions {
	return outlined(gtx, th, color.NRGBA{}, th.Pal.Divider, 4, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(th.Dp(28))
		gtx.Constraints.Min.Y = h
		return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Icon(gtx, th, n, 12, th.Pal.Muted) }),
				HSpace(th.Dp(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					l := label(th, 12.5, txt, th.Pal.Fg2)
					l.MaxLines = 1
					return VCenter(gtx, h, l.Layout)
				}),
			)
		})
	})
}

// PageHeader is the 54px bar on top of a main-window page: a title on the
// left, optional actions on the right and an edge-to-edge rule below.
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

// NavItem is a sidebar entry. The selected one is tinted, drawn in the
// primary text colour and medium weight and carries an accent bar on its
// leading edge, so selection never depends on colour alone (NFR-005).
func NavItem(gtx layout.Context, th *theme.Theme, c *widget.Clickable, n icon.Name, txt string, selected bool) layout.Dimensions {
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.SelectedOp(selected).Add(gtx.Ops)
			h := gtx.Dp(th.Dp(38))
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
			r := gtx.Dp(th.Dp(4))
			switch {
			case selected:
				rrect(gtx, r, Over(th.Pal.Sidebar, th.Pal.AccentSoft))
				bar := image.Rect(0, h/4, max(2, gtx.Dp(th.Dp(2))), h-h/4)
				paint.FillShape(gtx.Ops, th.Pal.AccentLine, clip.UniformRRect(bar, bar.Dx()/2).Op(gtx.Ops))
			case c.Hovered():
				rrect(gtx, r, th.Pal.Hover)
			}
			fg := th.Pal.Fg2
			if selected {
				fg = th.Pal.Fg
			}
			return layout.Inset{Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return VCenter(gtx, h, func(gtx layout.Context) layout.Dimensions { return Icon(gtx, th, n, 16, fg) })
					}),
					HSpace(th.Dp(10)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						l := label(th, 14, txt, fg)
						l.MaxLines = 1
						if selected {
							l.Font.Weight = font.Medium
						}
						return VCenter(gtx, h, l.Layout)
					}),
				)
			})
		})
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
		bg, fg, border = Over(th.Pal.Surface, th.Pal.AccentSoft), th.Pal.AccentText, th.Pal.AccentLine
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
	s := gtx.Dp(th.Dp(36))
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			gtx.Constraints = layout.Exact(image.Pt(s, s))
			paint.FillShape(gtx.Ops, bg, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
			if border.A != 0 {
				paint.FillShape(gtx.Ops, border, clip.Stroke{Path: clip.Ellipse{Max: image.Pt(s, s)}.Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
			}
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Icon(gtx, th, n, 16, fg)
			})
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
		bg, fg = Over(th.Pal.Bg, th.Pal.AccentSoft), th.Pal.AccentText
	}
	rrect(gtx, gtx.Dp(th.Dp(6)), bg)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		l := label(th, 15, "↖", fg)
		l.Font = font.Font{Typeface: theme.Display, Weight: font.Bold}
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

// Box draws w on a rounded fill with a 1px outline; radius is in
// prototype pixels.
func Box(gtx layout.Context, th *theme.Theme, bg, border color.NRGBA, radius float32, w layout.Widget) layout.Dimensions {
	return outlined(gtx, th, bg, border, radius, w)
}

func outlined(gtx layout.Context, th *theme.Theme, bg, border color.NRGBA, radius float32, w layout.Widget) layout.Dimensions {
	r := th.Dp(radius)
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			d := layout.Dimensions{Size: gtx.Constraints.Min}
			if bg.A != 0 {
				d = rrect(gtx, gtx.Dp(r), bg)
			}
			if border.A != 0 {
				widget.Border{Color: border, Width: 1, CornerRadius: r}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				})
			}
			return d
		}, w)
}

// Tint returns col with its alpha scaled by f, e.g. a stronger AccentSoft
// for message bubbles.
func Tint(col color.NRGBA, f float32) color.NRGBA {
	col.A = uint8(min(255, float32(col.A)*f))
	return col
}

// Over composites the translucent src onto the opaque dst in sRGB space,
// as CSS does. Gio blends in linear light, which makes the prototypes'
// low-alpha tints look much stronger on dark backgrounds, so tints are
// flattened before painting.
func Over(dst, src color.NRGBA) color.NRGBA {
	a := float32(src.A) / 255
	mix := func(d, s uint8) uint8 { return uint8(float32(d)*(1-a) + float32(s)*a + 0.5) }
	return color.NRGBA{R: mix(dst.R, src.R), G: mix(dst.G, src.G), B: mix(dst.B, src.B), A: 255}
}
