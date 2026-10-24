package wizard

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/openclick/internal/ui/kit"
	"github.com/xlsft/openclick/internal/ui/theme"
)

func monospace(th *theme.Theme, txt string) material.LabelStyle {
	l := material.Label(th.Theme, th.Sp(0.9), txt)
	l.Font.Typeface = font.Typeface("Go Mono")
	return l
}

// dropdown is a button showing the current choice that expands into the
// option list; Gio has no native select widget. UI goroutine only.
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
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(th.Theme, &d.toggle, cur+"  ▾")
			b.Background = th.Pal.Surface
			b.Color = th.Pal.Fg
			return kit.FocusRing(gtx, th, gtx.Focused(&d.toggle), func(gtx layout.Context) layout.Dimensions {
				return widget.Border{Color: th.Pal.Divider, Width: 1, CornerRadius: 4}.Layout(gtx, b.Layout)
			})
		}),
	}
	if d.open {
		for i, lbl := range labels {
			w.frame.texts = append(w.frame.texts, lbl)
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				o := &d.options[i]
				return kit.FocusRing(gtx, th, gtx.Focused(o), func(gtx layout.Context) layout.Dimensions {
					return o.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						txt := lbl
						if i == sel {
							txt = theme.SelectedMarker + " " + txt
						}
						return layout.Inset{Top: 6, Bottom: 6, Left: 12}.Layout(gtx, kit.Body(th, txt).Layout)
					})
				})
			}))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// field is a labelled single- or multi-line editor.
func field(w *Wizard, th *theme.Theme, label, hint string, ed *widget.Editor, enabled bool) layout.Widget {
	lbl := w.text(kit.Muted(th, label))
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(lbl),
				kit.VSpace(4),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if !enabled {
						gtx = gtx.Disabled()
					}
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					e := material.Editor(th.Theme, ed, hint)
					return kit.FocusRing(gtx, th, gtx.Focused(ed), func(gtx layout.Context) layout.Dimensions {
						return widget.Border{Color: th.Pal.Divider, Width: 1, CornerRadius: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return layout.UniformInset(8).Layout(gtx, e.Layout)
						})
					})
				}),
			)
		})
	}
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

// bar is a labelled progress bar; p in [0, 1].
func bar(th *theme.Theme, p float32) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		pb := material.ProgressBar(th.Theme, max(0, min(1, p)))
		pb.Height = unit.Dp(8 * th.Scale)
		pb.TrackColor = th.Pal.Divider
		return pb.Layout(gtx)
	}
}

// column stacks widgets vertically, e.g. radio choices that would not fit
// side by side at large text sizes.
func column(ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		ch := make([]layout.FlexChild, len(ws))
		for i, w := range ws {
			ch[i] = layout.Rigid(w)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	}
}

// row lays out widgets horizontally with spacing.
func row(ws ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		var ch []layout.FlexChild
		for i, w := range ws {
			if i > 0 {
				ch = append(ch, layout.Rigid(layout.Spacer{Width: 12}.Layout))
			}
			ch = append(ch, layout.Rigid(w))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	}
}
