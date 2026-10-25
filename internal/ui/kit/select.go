package kit

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// richRowH is the height of a two-line option in prototype pixels.
const richRowH = 60

// RichTag is a small label after an option's title, e.g. "Offline" or
// "Recommended".
type RichTag struct {
	Text string
	Kind TagKind
}

// RichOption is an option of a rich select: a title with tags, a value on
// the right (a size) and one line of description under it.
type RichOption struct {
	Title string
	Tags  []RichTag
	Meta  string
	Desc  string
}

// Texts are every string the option shows, in reading order.
func (o RichOption) Texts() []string {
	out := []string{o.Title}
	for _, t := range o.Tags {
		out = append(out, t.Text)
	}
	if o.Meta != "" {
		out = append(out, o.Meta)
	}
	if o.Desc != "" {
		out = append(out, o.Desc)
	}
	return out
}

// LayoutRich is Layout for options that need more than a label, such as
// speech models: the box shows the chosen option's title, tags and value;
// the list shows two-line rows between edge-to-edge dividers, the chosen
// one with a check. Update, Pick and keyboard handling are shared.
func (d *Dropdown) LayoutRich(gtx layout.Context, th *theme.Theme, opts []RichOption, sel int, placeholder string) layout.Dimensions {
	gtx, fg := d.prepare(gtx, th, len(opts))
	var cur RichOption
	desc := placeholder
	if sel >= 0 && sel < len(opts) {
		cur = opts[sel]
		desc = strings.Join(cur.Texts(), ", ")
	}
	dims := d.box(gtx, th, desc, 40, func(gtx layout.Context) layout.Dimensions {
		if cur.Title == "" {
			l := label(th, 14, placeholder, th.Pal.Muted)
			l.MaxLines = 1
			return l.Layout(gtx)
		}
		return richLine(gtx, th, cur, fg, false, false)
	})
	if d.open {
		d.typeAhead(gtx, func(i int) string { return opts[i].Title }, len(opts))
		d.popup(gtx, th, len(opts), sel, dims.Size, true, func(gtx layout.Context, i int) layout.Dimensions {
			return d.richOption(gtx, th, i, opts[i], i == sel)
		})
	}
	return dims
}

// richLine is the title, its tags and the right-aligned value. In the list
// every row keeps room for the check (inList), so the values line up.
func richLine(gtx layout.Context, th *theme.Theme, o RichOption, fg color.NRGBA, inList, check bool) layout.Dimensions {
	children := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := label(th, 14, o.Title, fg)
		l.Font.Weight = font.Medium
		l.MaxLines = 1
		return l.Layout(gtx)
	})}
	for _, t := range o.Tags {
		children = append(children, HSpace(th.Dp(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return Tag(gtx, th, t.Text, t.Kind)
		}))
	}
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
	}))
	if o.Meta != "" {
		children = append(children, HSpace(th.Dp(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := label(th, 12.5, o.Meta, th.Pal.Muted)
			l.MaxLines = 1
			return l.Layout(gtx)
		}))
	}
	if inList {
		mark := func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(th.Dp(14)), 0)}
		}
		if check {
			mark = IconW(th, icon.Check, 14, th.Pal.AccentText)
		}
		children = append(children, HSpace(th.Dp(10)), layout.Rigid(mark))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
}

func (d *Dropdown) richOption(gtx layout.Context, th *theme.Theme, i int, o RichOption, sel bool) layout.Dimensions {
	c := &d.options[i]
	h := gtx.Dp(th.Dp(richRowH))
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.SelectedOp(sel).Add(gtx.Ops)
		size := image.Pt(gtx.Constraints.Max.X, h)
		if c.Hovered() || gtx.Focused(c) {
			paint.FillShape(gtx.Ops, th.Pal.Hover, clip.Rect{Max: size}.Op())
		}
		if i > 0 {
			Divider(gtx, th)
		}
		gtx.Constraints = layout.Exact(size)
		// The insets line the title up with the box's own text and the
		// check with its arrow.
		layout.Inset{Left: th.Dp(12), Right: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return VCenter(gtx, gtx.Constraints.Max.Y, VStack(th, 4,
				func(gtx layout.Context) layout.Dimensions { return richLine(gtx, th, o, th.Pal.Fg, true, sel) },
				func(gtx layout.Context) layout.Dimensions {
					if o.Desc == "" {
						return layout.Dimensions{}
					}
					l := label(th, 12.5, o.Desc, th.Pal.Muted)
					l.MaxLines = 1
					return l.Layout(gtx)
				},
			))
		})
		return layout.Dimensions{Size: size}
	})
}
