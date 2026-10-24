package kit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/theme"
)

// Banner is the full-width notice bar across the top of a window, above
// every page: an icon, a title and a detail, an optional progress track,
// the actions and an optional close button. The update notice of the main
// and the setup window is one Banner, so it looks the same in both.
type Banner struct {
	// Status picks the tint and icon colour; StatusUnknown is the
	// informational accent tint.
	Status theme.Status
	Icon   icon.Name
	Title  string
	Detail string
	// Progress in [0, 1] draws a track under the text; negative draws none.
	Progress float32
	// Actions are laid out at the end of the bar, or under the text when
	// the window is narrow.
	Actions []layout.Widget
	// Close, when set, is the dismiss button; CloseLabel names it.
	Close      *widget.Clickable
	CloseLabel string
}

func (b Banner) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	bg, fg := th.Pal.AccentTint, th.Pal.AccentText
	switch b.Status {
	case theme.StatusWarn, theme.StatusError:
		bg, fg = tintOf(th.Pal, b.Status), th.Pal.Color(b.Status)
	case theme.StatusOK:
		fg = th.Pal.Color(b.Status)
	}
	var detail, progress layout.Widget
	if b.Detail != "" {
		detail = Muted(th, b.Detail).Layout
	}
	if b.Progress >= 0 {
		progress = func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(360)))
			return layout.Inset{Top: th.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return ProgressBar(gtx, th, 4, b.Progress)
			})
		}
	}
	text := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: th.Dp(1)}.Layout(gtx, IconW(th, b.Icon, 16, fg))
			}),
			HSpace(th.Dp(10)),
			layout.Flexed(1, VStack(th, 2, Medium(label(th, 13, b.Title, th.Pal.Fg)).Layout, detail, progress)),
		)
	}
	actions := func(gtx layout.Context) layout.Dimensions {
		ws := append([]layout.Widget(nil), b.Actions...)
		if b.Close != nil {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return IconButton(gtx, th, b.Close, icon.Close, b.CloseLabel)
			})
		}
		return HStack(th, 8, ws...)(gtx)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Background{}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions { return Fill(gtx, bg) },
				func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return Pad(th, 10, 20, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						if len(b.Actions) > 0 && gtx.Constraints.Max.X < gtx.Dp(th.Dp(640)) {
							return VStack(th, 10, text, actions)(gtx)
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, text),
							HSpace(th.Dp(16)),
							layout.Rigid(actions),
						)
					})(gtx)
				})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return Divider(gtx, th)
		}),
	)
}
