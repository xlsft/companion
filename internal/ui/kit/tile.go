package kit

import (
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Tile is a choice in a grid of services (the setup's providers and
// voices): a 30px mark with the service's logo, or its initial when it
// has none, a name and a caption, and an optional accent badge.
type Tile struct {
	Logo  icon.Name // "" draws Mark instead
	Mark  string    // one or two letters for a service without a logo
	Title string
	Sub   string
	Badge string // e.g. "by default"
}

// TileHeight is a tile's height in dp, for grids that show whole rows.
const TileHeight = 58

// Layout draws the tile as an option of e under key.
func (t Tile) Layout(gtx layout.Context, th *theme.Theme, e *widget.Enum, key string) layout.Dimensions {
	sel := e.Value == key
	hk, hovered := e.Hovered()
	fk, focused := e.Focused()
	border, bg := th.Pal.Border, th.Pal.Raised
	switch {
	case sel:
		border, bg = th.Pal.AccentLine, th.Pal.AccentSoft
	case hovered && hk == key && gtx.Enabled():
		bg = th.Pal.Hover
	}
	h := gtx.Dp(th.Dp(TileHeight))
	return FocusRing(gtx, th, focused && fk == key, func(gtx layout.Context) layout.Dimensions {
		return e.Layout(gtx, key, func(gtx layout.Context) layout.Dimensions {
			semantic.SelectedOp(sel).Add(gtx.Ops)
			semantic.DescriptionOp(t.Title).Add(gtx.Ops)
			gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
			return Box(gtx, th, bg, border, 6, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(12), Right: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return VCenter(gtx, h, t.mark(th)) }),
						HSpace(th.Dp(10)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							title := Medium(label(th, 14, t.Title, th.Pal.Fg))
							title.MaxLines = 1
							rows := []layout.Widget{title.Layout}
							if t.Sub != "" {
								sub := label(th, 12, t.Sub, th.Pal.Subtle)
								sub.MaxLines = 1
								rows = append(rows, sub.Layout)
							}
							return VCenter(gtx, h, VStack(th, 2, rows...))
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if t.Badge == "" {
								return layout.Dimensions{}
							}
							b := label(th, 11, t.Badge, th.Pal.AccentText)
							b.MaxLines = 1
							return VCenter(gtx, h, b.Layout)
						}),
					)
				})
			})
		})
	})
}

func (t Tile) mark(th *theme.Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(th.Dp(30))
		gtx.Constraints = layout.Exact(image.Pt(s, s))
		return Box(gtx, th, th.Pal.Surface, th.Pal.Divider, 6, func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if t.Logo != "" {
					return Icon(gtx, th, t.Logo, 16, th.Pal.Fg2)
				}
				m := label(th, 13, strings.ToUpper(t.Mark), th.Pal.Fg2)
				m.Font.Weight = font.Bold
				m.MaxLines = 1
				return m.Layout(gtx)
			})
		})
	}
}
