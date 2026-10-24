package wizard

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Wizard-only pieces of the prototypes that kit does not have: radio
// cards, the select box, chips and the small layout helpers the step
// bodies are built from. Sizes are the prototypes' CSS pixels, scaled
// with the text through theme.Theme.Dp like kit's.

// label is a plain text style at a prototype pixel size.
func label(th *theme.Theme, size float32, txt string, col color.NRGBA) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(size/float32(theme.BaseTextSize)), txt)
	l.Color = col
	return l
}

// caption is the 11.5px field and column label.
func caption(th *theme.Theme, txt string) material.LabelStyle {
	return label(th, 11.5, txt, th.Pal.Muted)
}

func medium(l material.LabelStyle) material.LabelStyle {
	l.Font.Weight = font.Medium
	return l
}

// soft is c mixed into the window background by frac in sRGB, as the
// prototypes' rgba() tints are. A translucent fill would be blended in
// linear space by Gio and come out much stronger.
func soft(th *theme.Theme, c color.NRGBA, frac float32) color.NRGBA {
	bg := th.Pal.Bg
	mix := func(a, b uint8) uint8 { return uint8(float32(a)*(1-frac) + float32(b)*frac + 0.5) }
	return color.NRGBA{R: mix(bg.R, c.R), G: mix(bg.G, c.G), B: mix(bg.B, c.B), A: 0xff}
}

func fillRect(gtx layout.Context, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Min}.Op())
}

func rrect(gtx layout.Context, r unit.Dp, c color.NRGBA) layout.Dimensions {
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(r)).Op(gtx.Ops))
	return layout.Dimensions{Size: gtx.Constraints.Min}
}

// box draws w on a rounded background with an optional 1px border; a
// zero colour leaves that part out.
func box(gtx layout.Context, th *theme.Theme, r float32, bg, border color.NRGBA, w layout.Widget) layout.Dimensions {
	rad := th.Dp(r)
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			if bg.A != 0 {
				rrect(gtx, rad, bg)
			}
			if border.A != 0 {
				widget.Border{Color: border, Width: 1, CornerRadius: rad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				})
			}
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}, w)
}

// surface is a Panel on the Surface fill: the prototypes' cards.
func surface(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	return kit.Panel(gtx, th, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				fillRect(gtx, th.Pal.Surface)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}, w)
	})
}

// card is a padded surface.
func card(gtx layout.Context, th *theme.Theme, pad float32, w layout.Widget) layout.Dimensions {
	return surface(gtx, th, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.UniformInset(th.Dp(pad)).Layout(gtx, w)
	})
}

// vstack lays children out top to bottom with gap dp between them; nil
// children are left out, so optional parts need no bookkeeping.
func vstack(th *theme.Theme, gap float32, ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var ch []layout.FlexChild
		for _, w := range ws {
			if w == nil {
				continue
			}
			if len(ch) > 0 {
				ch = append(ch, kit.VSpace(th.Dp(gap)))
			}
			ch = append(ch, layout.Rigid(w))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}
}

// hstack is vstack's horizontal sibling, vertically centred.
func hstack(th *theme.Theme, gap float32, ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var ch []layout.FlexChild
		for _, w := range ws {
			if w == nil {
				continue
			}
			if len(ch) > 0 {
				ch = append(ch, kit.HSpace(th.Dp(gap)))
			}
			ch = append(ch, layout.Rigid(w))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	}
}

// spread puts left at the start and right at the end of the full width.
func spread(left, right layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if left == nil {
					return layout.Dimensions{}
				}
				return left(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if right == nil {
					return layout.Dimensions{}
				}
				return right(gtx)
			}),
		)
	}
}

// grid lays items out in cols equal columns with gap dp between cells;
// each row is as tall as its tallest cell and cells fill that height.
func grid(th *theme.Theme, cols int, gap float32, items ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		g := gtx.Dp(th.Dp(gap))
		cw := (gtx.Constraints.Max.X - g*(cols-1)) / cols
		y := 0
		for start := 0; start < len(items); start += cols {
			row := items[start:min(start+cols, len(items))]
			// Measure first so every cell of the row gets the same height.
			h := 0
			for _, it := range row {
				c := gtx
				c.Constraints = layout.Constraints{Min: image.Pt(cw, 0), Max: image.Pt(cw, gtx.Constraints.Max.Y)}
				m := op.Record(gtx.Ops)
				h = max(h, it(c).Size.Y)
				m.Stop()
			}
			for i, it := range row {
				c := gtx
				c.Constraints = layout.Exact(image.Pt(cw, h))
				st := op.Offset(image.Pt(i*(cw+g), y)).Push(gtx.Ops)
				it(c)
				st.Pop()
			}
			y += h + g
		}
		if y > 0 {
			y -= g
		}
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
	}
}

// columns splits the width between a flexible left part and a fixed
// right one (right dp wide), or stacks them when the width is below
// minWide dp: the window is often narrower than the prototypes.
func columns(th *theme.Theme, right, gap, minWide float32, l, r layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(minWide)) {
			return vstack(th, gap*0.5, l, r)(gtx)
		}
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, l),
			kit.HSpace(th.Dp(gap)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				w := gtx.Dp(th.Dp(right))
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
				return r(gtx)
			}),
		)
	}
}

// fixedH lays w out vertically centred in a row of exactly h dp.
func fixedH(th *theme.Theme, h float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return kit.VCenter(gtx, gtx.Dp(th.Dp(h)), w)
	}
}

func iconW(th *theme.Theme, n icon.Name, size float32, col color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return kit.Icon(gtx, th, n, size, col) }
}

// disc is a filled circle of d dp with w centred on it.
func disc(th *theme.Theme, d float32, bg, border color.NRGBA, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(th.Dp(d))
		gtx.Constraints = layout.Exact(image.Pt(s, s))
		if bg.A != 0 {
			paint.FillShape(gtx.Ops, bg, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
		}
		if border.A != 0 {
			widget.Border{Color: border, Width: 1, CornerRadius: unit.Dp(float32(s) / 2 / gtx.Metric.PxPerDp)}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} })
		}
		if w == nil {
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}
		return layout.Center.Layout(gtx, w)
	}
}

// chip is the small accent tag of the STT card.
func chip(th *theme.Theme, txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return box(gtx, th, 4, soft(th, th.Pal.AccentLine, 0.14), color.NRGBA{}, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(8), Right: th.Dp(8)}.Layout(gtx,
				fixedH(th, 22, label(th, 11.5, txt, th.Pal.AccentText).Layout))
		})
	}
}

// statusText is a status as icon and word in its colour, without the
// Badge tint: the table and stage cards of the prototypes.
func statusText(th *theme.Theme, s theme.Status, txt string, size float32, col color.NRGBA) layout.Widget {
	return hstack(th, 6, iconW(th, kit.StatusIcon(s), size+2, col), label(th, size, txt, col).Layout)
}

// radioCard is one option of a radio group drawn as the prototypes'
// selectable card: accent outline and tint when chosen.
func radioCard(gtx layout.Context, th *theme.Theme, e *widget.Enum, key, title, sub string) layout.Dimensions {
	sel := e.Value == key
	hk, hovered := e.Hovered()
	fk, focused := e.Focused()
	border, bg := th.Pal.Divider, color.NRGBA{}
	if sel {
		border, bg = th.Pal.AccentLine, soft(th, th.Pal.AccentLine, 0.08)
	} else if hovered && hk == key && gtx.Enabled() {
		bg = th.Pal.Hover
	}
	return kit.FocusRing(gtx, th, focused && fk == key, func(gtx layout.Context) layout.Dimensions {
		return e.Layout(gtx, key, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return box(gtx, th, 4, bg, border, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: th.Dp(12), Bottom: th.Dp(12), Left: th.Dp(14), Right: th.Dp(14)}.Layout(gtx,
					func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(radioMark(th, sel)),
							kit.HSpace(th.Dp(10)),
							layout.Flexed(1, vstack(th, 2,
								medium(label(th, 14, title, th.Pal.Fg)).Layout,
								label(th, 12, sub, th.Pal.Muted).Layout)),
						)
					})
			})
		})
	})
}

// radioMark is the 14px radio circle; selection shows as a filled dot,
// not by colour alone.
func radioMark(th *theme.Theme, sel bool) layout.Widget {
	if !sel {
		return disc(th, 14, color.NRGBA{}, th.Pal.Border, nil)
	}
	return disc(th, 14, th.Pal.AccentLine, color.NRGBA{}, disc(th, 6, th.Pal.AccentFg, color.NRGBA{}, nil))
}

// field is a captioned kit.Field.
func field(w *Wizard, th *theme.Theme, cap, hint string, ed *widget.Editor, enabled bool) layout.Widget {
	c := w.text(caption(th, cap))
	return vstack(th, 4, c, func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		return kit.Field(gtx, th, ed, hint)
	})
}

// changed drains the editor's events and reports whether its text changed.
func changed(gtx layout.Context, ed *widget.Editor) bool {
	ch := false
	for {
		ev, ok := ed.Update(gtx)
		if !ok {
			return ch
		}
		if _, isChange := ev.(widget.ChangeEvent); isChange {
			ch = true
		}
	}
}

// bar is a rounded progress track h dp tall; p in [0, 1].
func bar(th *theme.Theme, h float32, p float32) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		p = min(max(p, 0), 1)
		hp := gtx.Dp(th.Dp(h))
		size := image.Pt(gtx.Constraints.Max.X, hp)
		defer clip.UniformRRect(image.Rectangle{Max: size}, hp/2).Push(gtx.Ops).Pop()
		paint.FillShape(gtx.Ops, th.Pal.Hover, clip.Rect{Max: size}.Op())
		paint.FillShape(gtx.Ops, th.Pal.AccentLine, clip.Rect{Max: image.Pt(int(float32(size.X)*p), hp)}.Op())
		return layout.Dimensions{Size: size}
	}
}

// maxWidth caps w's width at dp.
func maxWidth(th *theme.Theme, dp float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(dp)))
		gtx.Constraints.Min.X = min(gtx.Constraints.Min.X, gtx.Constraints.Max.X)
		return w(gtx)
	}
}
