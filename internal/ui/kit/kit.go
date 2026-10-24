// Package kit holds the widgets shared by the shell windows and the setup
// wizard, drawn after the design prototypes: type styles, buttons with a
// visible focus ring, bordered panels, full-width dividers, status badges
// and the capability report rows of the Diagnostics view.
//
// Sizes are the prototypes' CSS pixels scaled with the user's text scale
// (theme.Theme.Dp), so the layout keeps its proportions at every zoom.
package kit

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// px converts a prototype CSS size to a text-relative Sp.
func px(th *theme.Theme, v float32) unit.Sp { return th.Sp(v / float32(theme.BaseTextSize)) }

func label(th *theme.Theme, size float32, txt string, col color.NRGBA) material.LabelStyle {
	l := material.Label(th.Theme, px(th, size), txt)
	l.Color = col
	return l
}

// Heading is a page title: Unbounded, as in the prototypes.
func Heading(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 26, txt, th.Pal.Fg)
	l.Font = font.Font{Typeface: theme.Display, Weight: font.SemiBold}
	return l
}

// Title is a smaller display heading for panels and dialogs.
func Title(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 17, txt, th.Pal.Fg)
	l.Font = font.Font{Typeface: theme.Display, Weight: font.SemiBold}
	return l
}

// Subheading is a row or field title.
func Subheading(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 14, txt, th.Pal.Fg)
	l.Font.Weight = font.Medium
	return l
}

func Body(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 14, txt, th.Pal.Fg)
	l.LineHeightScale = 1.5
	return l
}

// Lead is the secondary paragraph under a heading.
func Lead(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 14, txt, th.Pal.Muted)
	l.LineHeightScale = 1.55
	return l
}

// Muted is a caption under a row title.
func Muted(th *theme.Theme, txt string) material.LabelStyle {
	return label(th, 12, txt, th.Pal.Muted)
}

// Small is the least prominent text: versions, timestamps, hints.
func Small(th *theme.Theme, txt string) material.LabelStyle {
	return label(th, 11, txt, th.Pal.Subtle)
}

// Code is monospace text for snippets and technical values.
func Code(th *theme.Theme, txt string) material.LabelStyle {
	l := label(th, 12.5, txt, th.Pal.Fg)
	l.Font.Typeface = theme.Mono
	l.LineHeightScale = 1.7
	return l
}

func VSpace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

func HSpace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Width: dp}.Layout)
}

// Logo is the ↖ mark and the word "companion", both Unbounded.
func Logo(gtx layout.Context, th *theme.Theme, size float32) layout.Dimensions {
	mark := label(th, size*1.2, "↖", th.Pal.AccentLine)
	mark.Font = font.Font{Typeface: theme.Display, Weight: font.SemiBold}
	word := label(th, size, "companion", th.Pal.Fg)
	word.Font = font.Font{Typeface: theme.Display, Weight: font.SemiBold}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(mark.Layout),
		HSpace(th.Dp(size*0.6)),
		layout.Rigid(word.Layout),
	)
}

// Icon draws a MingCute icon at a prototype pixel size.
func Icon(gtx layout.Context, th *theme.Theme, n icon.Name, size float32, col color.NRGBA) layout.Dimensions {
	return icon.Layout(gtx, n, th.Dp(size), col)
}

// VCenter lays w out at its natural height and centres it vertically in
// a row at least h pixels tall. layout.W would pass the row height on as a
// minimum, and labels then grow to it and draw their text at the top.
func VCenter(gtx layout.Context, h int, w layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.Y = 0
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	call := m.Stop()
	size := image.Pt(dims.Size.X, max(h, dims.Size.Y))
	off := (size.Y - dims.Size.Y) / 2
	defer op.Offset(image.Pt(0, off)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size, Baseline: dims.Baseline + size.Y - dims.Size.Y - off}
}

// Fill paints the whole minimum constraint with col and returns it.
func Fill(gtx layout.Context, col color.NRGBA) layout.Dimensions {
	paint.FillShape(gtx.Ops, col, clip.Rect{Max: gtx.Constraints.Min}.Op())
	return layout.Dimensions{Size: gtx.Constraints.Min}
}

func rrect(gtx layout.Context, r int, col color.NRGBA) layout.Dimensions {
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, r).Op(gtx.Ops))
	return layout.Dimensions{Size: gtx.Constraints.Min}
}

// Divider is a 1px rule across the full available width. Dividers always
// run edge to edge: inset rules are not part of the design.
func Divider(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, max(1, gtx.Dp(1)))
	paint.FillShape(gtx.Ops, th.Pal.Divider, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

// VDivider is a 1px vertical rule across the full available height.
func VDivider(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	size := image.Pt(max(1, gtx.Dp(1)), gtx.Constraints.Max.Y)
	paint.FillShape(gtx.Ops, th.Pal.Divider, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

// FocusRing draws a 2dp accent outline around w when focused, so keyboard
// focus is visible beyond material's faint hover tint (FR-UI-007).
func FocusRing(gtx layout.Context, th *theme.Theme, focused bool, w layout.Widget) layout.Dimensions {
	if !focused {
		return w(gtx)
	}
	return widget.Border{Color: th.Pal.AccentLine, Width: 2, CornerRadius: th.Dp(4)}.Layout(gtx, w)
}

// ButtonKind picks a button's look.
type ButtonKind int

const (
	// Primary is the step's main action: violet fill.
	Primary ButtonKind = iota
	// Secondary is any other action: outlined on a raised fill.
	Secondary
	// Ghost is a quiet action in toolbars and sidebars.
	Ghost
)

// ButtonStyle is a text button, optionally with a leading icon.
type ButtonStyle struct {
	Kind  ButtonKind
	Label string
	Icon  icon.Name // "" for none
	// Small is the 30px in-row size; the default is the 36px footer size.
	Small bool
}

func (b ButtonStyle) Layout(gtx layout.Context, th *theme.Theme, c *widget.Clickable) layout.Dimensions {
	disabled := !gtx.Enabled()
	h, pad, size := float32(36), float32(14), float32(14)
	if b.Small {
		h, pad, size = 30, 10, 13
	}
	if b.Kind == Primary {
		pad += 2
	}
	bg, fg, border := th.Pal.Raised, th.Pal.Fg, th.Pal.Divider
	switch b.Kind {
	case Primary:
		bg, fg, border = th.Pal.Accent, th.Pal.AccentFg, color.NRGBA{}
		if disabled {
			bg, fg = th.Pal.AccentDisabled, th.Pal.FgDisabled
		}
	case Ghost:
		bg, fg, border = color.NRGBA{}, th.Pal.Muted, color.NRGBA{}
	}
	if !disabled && c.Hovered() {
		switch b.Kind {
		case Primary:
			bg = th.Pal.AccentLine
		default:
			bg = th.Pal.Hover
		}
	}
	if disabled && b.Kind != Primary {
		fg = th.Pal.Subtle
	}
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = gtx.Dp(th.Dp(h))
			r := gtx.Dp(th.Dp(4))
			return layout.Background{}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					d := rrect(gtx, r, bg)
					if border.A != 0 {
						widget.Border{Color: border, Width: 1, CornerRadius: th.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: gtx.Constraints.Min}
						})
					}
					return d
				},
				func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Left: th.Dp(pad), Right: th.Dp(pad)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if b.Icon == "" {
									return layout.Dimensions{}
								}
								return layout.Inset{Right: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return Icon(gtx, th, b.Icon, size, fg)
								})
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return VCenter(gtx, gtx.Dp(th.Dp(h)), func(gtx layout.Context) layout.Dimensions {
									l := label(th, size, b.Label, fg)
									l.Font.Weight = font.Medium
									l.MaxLines = 1
									return l.Layout(gtx)
								})
							}),
						)
					})
				})
		})
	})
}

// Button is a primary button with a visible focus ring.
func Button(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	return ButtonStyle{Kind: Primary, Label: label}.Layout(gtx, th, b)
}

// SecondaryButton is an outlined button.
func SecondaryButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	return ButtonStyle{Kind: Secondary, Label: label}.Layout(gtx, th, b)
}

// IconButton is a square, borderless button showing only an icon; desc is
// its accessible name.
func IconButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, n icon.Name, desc string) layout.Dimensions {
	col := th.Pal.Muted
	return FocusRing(gtx, th, gtx.Focused(c), func(gtx layout.Context) layout.Dimensions {
		return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(desc).Add(gtx.Ops)
			s := gtx.Dp(th.Dp(32))
			gtx.Constraints = layout.Exact(image.Pt(s, s))
			if c.Hovered() {
				rrect(gtx, gtx.Dp(th.Dp(4)), th.Pal.Hover)
				col = th.Pal.Fg
			}
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Icon(gtx, th, n, 16, col)
			})
		})
	})
}

// Panel is a bordered, rounded group of rows with no inner padding, so the
// rows' own dividers reach its edges.
func Panel(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	r := th.Dp(6)
	m := op.Record(gtx.Ops)
	dims := func() layout.Dimensions {
		defer clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Max}, gtx.Dp(r)).Push(gtx.Ops).Pop()
		return w(gtx)
	}()
	call := m.Stop()
	gtx.Constraints = layout.Exact(dims.Size)
	defer clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(r)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	widget.Border{Color: th.Pal.Divider, Width: 1, CornerRadius: r}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: dims.Size}
	})
	return dims
}

// Card is a padded Panel for free-form content.
func Card(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	return Panel(gtx, th, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(th.Dp(16)).Layout(gtx, w)
	})
}

// Rows lays rows out vertically inside a Panel with full-width dividers
// between them.
func Rows(gtx layout.Context, th *theme.Theme, rows ...layout.Widget) layout.Dimensions {
	return Panel(gtx, th, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 2*len(rows))
		for i, r := range rows {
			if i > 0 {
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Divider(gtx, th) }))
			}
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return r(gtx)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// IconTile is the 40px rounded square holding a row's icon.
func IconTile(gtx layout.Context, th *theme.Theme, n icon.Name, col color.NRGBA) layout.Dimensions {
	s := gtx.Dp(th.Dp(40))
	gtx.Constraints = layout.Exact(image.Pt(s, s))
	rrect(gtx, gtx.Dp(th.Dp(6)), th.Pal.Raised)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return Icon(gtx, th, n, 18, col)
	})
}

// Badge shows a status as an icon and a word on a soft tint, so the state
// never depends on colour alone (NFR-005).
func Badge(gtx layout.Context, th *theme.Theme, s theme.Status, txt string) layout.Dimensions {
	fg, bg := th.Pal.Color(s), th.Pal.Raised
	switch s {
	case theme.StatusOK:
		bg = th.Pal.OKSoft
	case theme.StatusWarn:
		bg = th.Pal.WarnSoft
	case theme.StatusError:
		bg = th.Pal.ErrSoft
	}
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions { return rrect(gtx, gtx.Dp(th.Dp(4)), bg) },
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = gtx.Dp(th.Dp(24))
			return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return Icon(gtx, th, StatusIcon(s), 12, fg) }),
					HSpace(th.Dp(6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return VCenter(gtx, gtx.Dp(th.Dp(24)), label(th, 12, txt, fg).Layout)
					}),
				)
			})
		})
}

// StatusIcon is the shape cue for a status.
func StatusIcon(s theme.Status) icon.Name {
	switch s {
	case theme.StatusOK:
		return icon.Check
	case theme.StatusWarn:
		return icon.Warning
	case theme.StatusError:
		return icon.Close
	}
	return icon.Time
}

// Progress is the 4px rounded progress track; frac is clamped to [0, 1].
func Progress(gtx layout.Context, th *theme.Theme, frac float32) layout.Dimensions {
	frac = min(max(frac, 0), 1)
	h := gtx.Dp(th.Dp(4))
	size := image.Pt(gtx.Constraints.Max.X, h)
	defer clip.UniformRRect(image.Rectangle{Max: size}, h/2).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Pal.Hover, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, th.Pal.AccentLine, clip.Rect{Max: image.Pt(int(float32(size.X)*frac), h)}.Op())
	return layout.Dimensions{Size: size}
}

// CodeBlock is a bordered monospace block; lines keep their indentation.
func CodeBlock(gtx layout.Context, th *theme.Theme, src string) layout.Dimensions {
	r := th.Dp(4)
	return widget.Border{Color: th.Pal.Divider, Width: 1, CornerRadius: r}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions { return rrect(gtx, gtx.Dp(r), th.Pal.Code) },
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(12), Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := Code(th, src)
					l.Color = th.Pal.Fg2
					l.WrapPolicy = text.WrapGraphemes
					return l.Layout(gtx)
				})
			})
	})
}

// Field draws an editor as the prototypes' input: 36px, outlined, raised.
// The border turns accent while the editor has focus.
func Field(gtx layout.Context, th *theme.Theme, ed *widget.Editor, hint string) layout.Dimensions {
	border := th.Pal.Divider
	if gtx.Focused(ed) {
		border = th.Pal.AccentLine
	}
	r := th.Dp(4)
	return widget.Border{Color: border, Width: 1, CornerRadius: r}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y = gtx.Dp(th.Dp(36))
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions { return rrect(gtx, gtx.Dp(r), th.Pal.Raised) },
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(12), Right: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return VCenter(gtx, gtx.Dp(th.Dp(36)), func(gtx layout.Context) layout.Dimensions {
						e := material.Editor(th.Theme, ed, hint)
						e.TextSize = px(th, 14)
						e.Color = th.Pal.Fg
						e.HintColor = th.Pal.Subtle
						e.SelectionColor = th.Pal.AccentSoft
						if ed.Mask != 0 {
							e.Font.Typeface = theme.Mono
						}
						return e.Layout(gtx)
					})
				})
			})
	})
}

// OrDash shows an em dash for an empty value.
func OrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
