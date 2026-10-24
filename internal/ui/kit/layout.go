package kit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/xlsft/companion/internal/ui/theme"
)

// Layout helpers the pages are built from. Gaps are prototype pixels; nil
// widgets are left out, so optional parts need no bookkeeping.

// VStack lays ws out top to bottom with gap between them.
func VStack(th *theme.Theme, gap float32, ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		ch := make([]layout.FlexChild, 0, 2*len(ws))
		for _, w := range ws {
			if w == nil {
				continue
			}
			if len(ch) > 0 && gap > 0 {
				ch = append(ch, VSpace(th.Dp(gap)))
			}
			ch = append(ch, layout.Rigid(w))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}
}

// HStack is VStack's horizontal sibling, vertically centred.
func HStack(th *theme.Theme, gap float32, ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		ch := make([]layout.FlexChild, 0, 2*len(ws))
		for _, w := range ws {
			if w == nil {
				continue
			}
			if len(ch) > 0 && gap > 0 {
				ch = append(ch, HSpace(th.Dp(gap)))
			}
			ch = append(ch, layout.Rigid(w))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	}
}

// Spread puts left at the start and right at the end of the full width,
// both vertically centred.
func Spread(left, right layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if left == nil {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
				}
				gtx.Constraints.Min.X = 0
				d := left(gtx)
				d.Size.X = gtx.Constraints.Max.X
				return d
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

// Grid lays items out in cols equal columns with gap between cells. Each
// row is as tall as its tallest cell and every cell gets that height, so
// cards in a row line up; cells centre or fill their content themselves.
func Grid(th *theme.Theme, cols int, gap float32, items ...layout.Widget) layout.Widget {
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

// Flow is a Grid with as many columns (at most len(items)) as fit cells
// of at least minCell.
func Flow(th *theme.Theme, minCell, gap float32, items ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		cell := gtx.Dp(th.Dp(minCell + gap))
		cols := min(max(1, (gtx.Constraints.Max.X+gtx.Dp(th.Dp(gap)))/cell), len(items))
		return Grid(th, cols, gap, items...)(gtx)
	}
}

// Columns splits the width between a flexible left part and a fixed
// right one (right wide), or stacks them when the width is below minWide:
// the window is often narrower than the prototypes.
func Columns(th *theme.Theme, right, gap, minWide float32, l, r layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(minWide)) {
			return VStack(th, gap*0.5, l, r)(gtx)
		}
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, l),
			HSpace(th.Dp(gap)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				w := gtx.Dp(th.Dp(right))
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
				return r(gtx)
			}),
		)
	}
}

// FixedH lays w out vertically centred in a row h tall.
func FixedH(th *theme.Theme, h float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return VCenter(gtx, gtx.Dp(th.Dp(h)), w)
	}
}

// MaxWidth caps w's width at dp.
func MaxWidth(th *theme.Theme, dp float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(dp)))
		gtx.Constraints.Min.X = min(gtx.Constraints.Min.X, gtx.Constraints.Max.X)
		return w(gtx)
	}
}

// Centered centres a widget that takes its natural width.
func Centered(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx, layout.Rigid(w))
	}
}

// Pad insets w by vertical and horizontal prototype pixels.
func Pad(th *theme.Theme, v, h float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: th.Dp(v), Bottom: th.Dp(v), Left: th.Dp(h), Right: th.Dp(h)}.Layout(gtx, w)
	}
}

// PageMargin is the side margin of a page body: the prototypes' value,
// shrinking in narrow windows and at large text sizes.
func PageMargin(gtx layout.Context, th *theme.Theme, proto float32) unit.Dp {
	return min(th.Dp(proto), unit.Dp(float32(gtx.Constraints.Max.X)/14/gtx.Metric.PxPerDp))
}
