package wizard

import (
	"image"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// The wizard draws only kit components; these wrappers add what the
// wizard needs on top: every shown text and offered action is recorded
// for the frame, so tests can assert there is no bypass and no secret or
// environment name on screen.

// text records a label for the frame and returns its widget.
func (w *Wizard) text(l material.LabelStyle) layout.Widget {
	w.frame.texts = append(w.frame.texts, l.Text)
	return l.Layout
}

// field is a captioned kit.Field.
func field(w *Wizard, th *theme.Theme, cap, hint string, ed *widget.Editor, enabled bool) layout.Widget {
	w.frame.texts = append(w.frame.texts, cap)
	return kit.Labeled(th, cap, func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		return kit.Field(gtx, th, ed, hint)
	})
}

// dropdown lays out a kit.Dropdown as a choice of the step.
func (w *Wizard) dropdown(gtx layout.Context, th *theme.Theme, d *kit.Dropdown, labels []string, sel int, placeholder string, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, ActChoose)
	cur := placeholder
	if sel >= 0 && sel < len(labels) {
		cur = labels[sel]
	}
	w.frame.texts = append(w.frame.texts, cur)
	if d.Opened() {
		w.frame.texts = append(w.frame.texts, labels...)
	}
	if !enabled {
		gtx = gtx.Disabled()
	}
	return d.Layout(gtx, th, labels, sel, placeholder)
}

// richDropdown lays out a rich kit.Dropdown as a choice of the step.
func (w *Wizard) richDropdown(gtx layout.Context, th *theme.Theme, d *kit.Dropdown, opts []kit.RichOption, sel int, placeholder string, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, ActChoose)
	if sel >= 0 && sel < len(opts) {
		w.frame.texts = append(w.frame.texts, opts[sel].Texts()...)
	} else {
		w.frame.texts = append(w.frame.texts, placeholder)
	}
	if d.Opened() {
		for _, o := range opts {
			w.frame.texts = append(w.frame.texts, o.Texts()...)
		}
	}
	if !enabled {
		gtx = gtx.Disabled()
	}
	return d.LayoutRich(gtx, th, opts, sel, placeholder)
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

// card is a kit.Card.
func card(th *theme.Theme, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return kit.Card(gtx, th, w) }
}

// label records a caption shown by a kit component and returns it.
func (w *Wizard) label(s string) string {
	w.frame.texts = append(w.frame.texts, s)
	return s
}

// statusText is one status line: a mark in the status's colour and text.
func statusText(w *Wizard, th *theme.Theme, st theme.Status, txt string) layout.Widget {
	w.frame.texts = append(w.frame.texts, txt)
	return func(gtx layout.Context) layout.Dimensions {
		col, ic := th.Pal.Muted, icon.Time
		fg := th.Pal.Muted
		switch st {
		case theme.StatusOK:
			col, ic, fg = th.Pal.OK, icon.CheckCircle, th.Pal.Fg
		case theme.StatusError:
			col, ic, fg = th.Pal.Error, icon.Warning, th.Pal.Fg
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(kit.IconW(th, ic, 15, col)),
			kit.HSpace(th.Dp(8)),
			layout.Flexed(1, kit.Text(th, 13, txt, fg).Layout),
		)
	}
}

// panel is a screen's form block: the surface card of the prototype.
func panel(th *theme.Theme, w layout.Widget) layout.Widget { return card(th, w) }

// tileGrid lays out tiles cols per row as options of e. Two rows show;
// more scroll inside the grid, so a long list does not push the form down.
func (w *Wizard) tileGrid(th *theme.Theme, e *widget.Enum, list *widget.List, keys []string, tiles []kit.Tile, cols int, enabled bool) layout.Widget {
	for _, t := range tiles {
		w.frame.texts = append(w.frame.texts, t.Title)
	}
	w.frame.actions = append(w.frame.actions, ActChoose)
	n := (len(tiles) + cols - 1) / cols
	return func(gtx layout.Context) layout.Dimensions {
		if !enabled {
			gtx = gtx.Disabled()
		}
		gap := th.Dp(8)
		gtx.Constraints.Max.Y = gtx.Dp(th.Dp(2*kit.TileHeight)) + gtx.Dp(gap)
		list.Axis = layout.Vertical
		return material.List(th.Theme, list).Layout(gtx, n, func(gtx layout.Context, r int) layout.Dimensions {
			in := layout.Inset{Right: th.Dp(10)}
			if r > 0 {
				in.Top = gap
			}
			return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				children := make([]layout.FlexChild, 0, 2*cols)
				for c := range cols {
					if c > 0 {
						children = append(children, kit.HSpace(gap))
					}
					i := r*cols + c
					children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						if i >= len(tiles) {
							return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
						}
						return tiles[i].Layout(gtx, th, e, keys[i])
					}))
				}
				return layout.Flex{}.Layout(gtx, children...)
			})
		})
	}
}
