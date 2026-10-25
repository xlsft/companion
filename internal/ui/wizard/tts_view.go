package wizard

import (
	"gioui.org/layout"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// storedKey stands in for a key field once a key is stored: the key never
// comes back to the UI, only the fact that it exists. Replace shows the
// editor again.
func storedKey(w *Wizard, th *theme.Theme, l i18n.Lang, title string, enabled bool) layout.Widget {
	w.frame.texts = append(w.frame.texts, title)
	saved := kit.Text(th, 14, i18n.T(l, "wiz.byok.key.saved"), th.Pal.Muted)
	saved.MaxLines = 1
	txt := w.text(saved)
	return kit.Labeled(th, title, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Box(gtx, th, th.Pal.Raised, th.Pal.Divider, 4, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: th.Dp(12), Right: th.Dp(3)}.Layout(gtx, kit.FixedH(th, 36, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(kit.IconW(th, icon.Lock, 14, th.Pal.Muted)),
					kit.HSpace(th.Dp(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = 0
						return txt(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return w.btn(gtx, th, ActReplaceKey, kit.ButtonStyle{Kind: kit.Ghost, Small: true, Label: i18n.T(l, "wiz.byok.key.replace")}, enabled)
					}),
				)
			}))
		})
	})
}
