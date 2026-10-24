package kit

import (
	"image/color"

	"gioui.org/layout"
	"gioui.org/text"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// StatusRow is the row of a status list (the wizard's permissions, its
// failure explanations and final-check stages): icon tile, title, caption
// and an optional fix line, a status badge, and an optional body under
// the text. A row that needs the user is tinted.
type StatusRow struct {
	Icon icon.Name
	// IconColor colours the tile's icon; zero picks the accent, or the
	// status colour on a tinted row.
	IconColor color.NRGBA
	Title     string
	Caption   string
	// Detail is an optional third line, e.g. how to fix a failure.
	Detail string
	Status theme.Status
	// Badge is the status word; "" leaves the badge out.
	Badge string
	// Tint marks the whole row with its warning or error status.
	Tint bool
	// Body is laid out under the text, aligned with it.
	Body layout.Widget
}

func (r StatusRow) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	bg := th.Pal.Surface
	ic := r.IconColor
	tile := th.Pal.Raised
	if r.Tint {
		bg = tintOf(th.Pal, r.Status)
		if r.Status == theme.StatusWarn || r.Status == theme.StatusError {
			// The tile wears the badge's colours, so the row reads as one
			// status at a glance.
			tile = softOf(th.Pal, r.Status)
			if ic.A == 0 {
				ic = th.Pal.Color(r.Status)
			}
		}
	}
	if ic.A == 0 {
		ic = th.Pal.AccentText
	}
	var cap, detail layout.Widget
	if r.Caption != "" {
		cap = Muted(th, r.Caption).Layout
	}
	if r.Detail != "" {
		detail = label(th, 13, r.Detail, th.Pal.Fg2).Layout
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return IconTileOn(gtx, th, r.Icon, ic, tile) }),
			HSpace(th.Dp(16)),
			layout.Flexed(1, VStack(th, 2, Medium(label(th, 14, r.Title, th.Pal.Fg)).Layout, cap, detail)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if r.Badge == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: th.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return Badge(gtx, th, r.Status, r.Badge)
				})
			}),
		)
	}
	var body layout.Widget
	if r.Body != nil {
		// Aligned with the text: the 40px tile plus its 16px gap.
		body = func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(56)}.Layout(gtx, r.Body)
		}
	}
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions { return Fill(gtx, bg) },
		func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return Pad(th, 16, 20, VStack(th, 14, head, body))(gtx)
		})
}

// Alert is an inline message block for a failed action or a warning: a
// status icon, a title and an optional detail on the status tint. Errors
// are always shown this way, never as bare red text.
func Alert(gtx layout.Context, th *theme.Theme, s theme.Status, title, detail string) layout.Dimensions {
	var d layout.Widget
	if detail != "" {
		d = Muted(th, detail).Layout
	}
	return panel(gtx, th, tintOf(th.Pal, s), Pad(th, 12, 14, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				// Level with the title's first line.
				return layout.Inset{Top: th.Dp(1)}.Layout(gtx, IconW(th, StatusIcon(s), 16, th.Pal.Color(s)))
			}),
			HSpace(th.Dp(10)),
			layout.Flexed(1, VStack(th, 2, Medium(label(th, 13, title, th.Pal.Fg)).Layout, d)),
		)
	}))
}

// CheckLine is a row of a check list: status icon, name and a detail on
// the right, h prototype pixels tall.
func CheckLine(th *theme.Theme, h float32, s theme.Status, name, right string) layout.Widget {
	col := th.Pal.Color(s)
	if s == theme.StatusUnknown {
		col = th.Pal.Subtle
	}
	return FixedH(th, h, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(IconW(th, StatusIcon(s), 16, col)),
			HSpace(th.Dp(10)),
			layout.Flexed(1, label(th, 14, name, th.Pal.Fg).Layout),
			HSpace(th.Dp(10)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(200)))
				l := label(th, 12, right, th.Pal.Muted)
				l.Alignment = text.End
				return l.Layout(gtx)
			}),
		)
	})
}

// SettingRow is a settings line: title and description on the left, the
// control on the right, or under the text when the row is narrow. A
// setting that is not implemented yet carries soon (the tag text) and a
// disabled control.
type SettingRow struct {
	Title, Desc string
	// Soon, when set, is the tag shown beside the title.
	Soon    string
	Control layout.Widget
}

func (r SettingRow) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	tfg := th.Pal.Fg
	if r.Soon != "" {
		tfg = th.Pal.Fg2
	}
	title := Medium(label(th, 14, r.Title, tfg)).Layout
	if r.Soon != "" {
		t := title
		title = HStack(th, 8, t, func(gtx layout.Context) layout.Dimensions { return Tag(gtx, th, r.Soon, TagNeutral) })
	}
	var desc layout.Widget
	if r.Desc != "" {
		d := label(th, 12.5, r.Desc, th.Pal.Muted)
		d.LineHeightScale = 1.4
		desc = d.Layout
	}
	txt := VStack(th, 3, title, desc)
	ctl := func(gtx layout.Context) layout.Dimensions {
		if r.Soon != "" {
			gtx = gtx.Disabled()
		}
		return r.Control(gtx)
	}
	return Pad(th, 16, 20, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		if r.Control == nil {
			return txt(gtx)
		}
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(520)) {
			return VStack(th, 12, txt, ctl)(gtx)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, txt),
			HSpace(th.Dp(24)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = 0
				gtx.Constraints.Max.X = gtx.Constraints.Max.X * 3 / 5
				return ctl(gtx)
			}),
		)
	})(gtx)
}
