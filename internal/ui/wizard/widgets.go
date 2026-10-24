package wizard

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// dropdown is the prototypes' select box that expands into the option
// list; Gio has no native select widget. UI goroutine only.
type dropdown struct {
	open    bool
	toggle  widget.Clickable
	options []widget.Clickable
}

// update returns the index chosen since the last frame, or -1.
func (d *dropdown) update(gtx layout.Context, n int) int {
	if d.toggle.Clicked(gtx) {
		d.open = !d.open
	}
	if len(d.options) < n {
		d.options = append(d.options, make([]widget.Clickable, n-len(d.options))...)
	}
	for i := range n {
		if d.options[i].Clicked(gtx) {
			d.open = false
			return i
		}
	}
	return -1
}

// layout draws the dropdown; labels[sel] is the current value (sel may be
// -1 for none, then placeholder is shown).
func (d *dropdown) layout(w *Wizard, gtx layout.Context, th *theme.Theme, labels []string, sel int, placeholder string, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, ActChoose)
	cur := placeholder
	if sel >= 0 && sel < len(labels) {
		cur = labels[sel]
	}
	w.frame.texts = append(w.frame.texts, cur)
	if !enabled || len(labels) == 0 {
		gtx = gtx.Disabled()
		d.open = false
	}
	if len(d.options) < len(labels) {
		d.options = append(d.options, make([]widget.Clickable, len(labels)-len(d.options))...)
	}
	fg := th.Pal.Fg
	if !gtx.Enabled() {
		fg = th.Pal.Subtle
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	head := func(gtx layout.Context) layout.Dimensions {
		return kit.FocusRing(gtx, th, gtx.Focused(&d.toggle), func(gtx layout.Context) layout.Dimensions {
			return d.toggle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				border := th.Pal.Divider
				if d.open {
					border = th.Pal.AccentLine
				}
				bg := th.Pal.Bg
				if d.toggle.Hovered() && gtx.Enabled() {
					bg = th.Pal.Hover
				}
				return box(gtx, th, 4, bg, border, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, fixedH(th, 36, func(gtx layout.Context) layout.Dimensions {
						arrow := icon.Down
						if d.open {
							arrow = icon.Up
						}
						return spread(func(gtx layout.Context) layout.Dimensions {
							l := label(th, 14, cur, fg)
							l.MaxLines = 1
							return l.Layout(gtx)
						}, iconW(th, arrow, 14, th.Pal.Muted))(gtx)
					}))
				})
			})
		})
	}
	if !d.open {
		return head(gtx)
	}
	opts := make([]layout.Widget, len(labels))
	for i, lbl := range labels {
		w.frame.texts = append(w.frame.texts, lbl)
		opts[i] = func(gtx layout.Context) layout.Dimensions {
			o := &d.options[i]
			return kit.FocusRing(gtx, th, gtx.Focused(o), func(gtx layout.Context) layout.Dimensions {
				return o.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					if o.Hovered() {
						fillRect(gtx, th.Pal.Hover)
					}
					// The chosen option carries a check, not only a tint.
					mark := layout.Spacer{Width: th.Dp(14), Height: th.Dp(14)}.Layout
					if i == sel {
						mark = iconW(th, icon.Check, 14, th.Pal.AccentText)
					}
					return layout.Inset{Left: th.Dp(10), Right: th.Dp(10)}.Layout(gtx, fixedH(th, 34,
						hstack(th, 8, mark, label(th, 14, lbl, th.Pal.Fg).Layout)))
				})
			})
		}
	}
	return vstack(th, 4, head, func(gtx layout.Context) layout.Dimensions {
		return kit.Rows(gtx, th, opts...)
	})(gtx)
}
