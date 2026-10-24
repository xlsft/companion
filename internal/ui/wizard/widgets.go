package wizard

import (
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

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

// alert is an inline kit.Alert; detail may be empty.
func alert(w *Wizard, th *theme.Theme, s theme.Status, title, detail string) layout.Widget {
	w.frame.texts = append(w.frame.texts, title)
	if detail != "" {
		w.frame.texts = append(w.frame.texts, detail)
	}
	return func(gtx layout.Context) layout.Dimensions { return kit.Alert(gtx, th, s, title, detail) }
}

// statusRow records a kit.StatusRow.
func (w *Wizard) statusRow(th *theme.Theme, r kit.StatusRow) layout.Widget {
	for _, s := range []string{r.Title, r.Caption, r.Detail, r.Badge} {
		if s != "" {
			w.frame.texts = append(w.frame.texts, s)
		}
	}
	return func(gtx layout.Context) layout.Dimensions { return r.Layout(gtx, th) }
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

// rows is a full-width kit.Rows.
func rows(th *theme.Theme, rs ...layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return kit.Rows(gtx, th, rs...) }
}

// card is a kit.Card.
func card(th *theme.Theme, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return kit.Card(gtx, th, w) }
}
