package wizard

import (
	"image/color"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// permissionCaps are the rows of the permissions step (FR-ONB-005).
var permissionCaps = []contracts.Capability{
	contracts.CapMicrophone,
	contracts.CapScreenCapture,
	contracts.CapGlobalHotkey,
	contracts.CapOverlayClickThrough,
}

var permissionIcons = map[contracts.Capability]icon.Name{
	contracts.CapMicrophone:          icon.Mic,
	contracts.CapScreenCapture:       icon.Computer,
	contracts.CapGlobalHotkey:        icon.Keyboard,
	contracts.CapOverlayClickThrough: icon.Layers,
}

type permState struct {
	copied bool // UI goroutine only
}

func (p *permState) update(w *Wizard, gtx layout.Context, s snapshot) {
	if w.checkClicked(gtx) {
		p.copied = false
		w.runCheck(readiness.StepPermissions, nil, nil)
	}
	if w.clicked(gtx, ActCopy) && w.cfg.Backends.Permissions != nil {
		txt := w.cfg.Backends.Permissions.HotkeyInstructions()
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(txt))})
		p.copied = true
	}
}

// permStatus maps a permission detail code to a status and word key.
func permStatus(v string) (theme.Status, string) {
	switch v {
	case readiness.PermGranted, readiness.DetailOK:
		return theme.StatusOK, "wiz.perm.status.granted"
	case readiness.PermFallback:
		return theme.StatusWarn, "wiz.perm.status.fallback"
	case readiness.PermNeedsPermission:
		return theme.StatusWarn, "wiz.perm.status.needs_permission"
	case readiness.PermNeedsBinding:
		return theme.StatusWarn, "wiz.perm.status.needs_compositor_binding"
	case readiness.PermDenied:
		return theme.StatusError, "wiz.perm.status.denied"
	case readiness.PermUnavailable:
		return theme.StatusError, "wiz.perm.status.unavailable"
	case "":
		return theme.StatusUnknown, "wiz.perm.status.todo"
	}
	return theme.StatusError, "wiz.perm.status.failed"
}

func (p *permState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	r, have := s.result(readiness.StepPermissions)
	rows := make([]layout.Widget, 0, len(permissionCaps))
	for _, c := range permissionCaps {
		code := ""
		if have {
			code = r.Detail[string(c)]
		}
		rows = append(rows, p.row(w, th, l, c, code, have))
	}
	return stepBody{maxW: 728, rows: []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Rows(gtx, th, rows...)
	}}}
}

// row is one permission: icon tile, name and caption, status badge. A
// permission that needs the user gets a tinted row and, for the hotkey,
// the shortcut to add.
func (p *permState) row(w *Wizard, th *theme.Theme, l i18n.Lang, c contracts.Capability, code string, have bool) layout.Widget {
	ds, key := permStatus(code)
	word := i18n.T(l, key)
	if key == "wiz.perm.status.failed" {
		word += " (" + code + ")"
	}
	why := i18n.T(l, "wiz.perm."+string(c)+".why")
	binding := c == contracts.CapGlobalHotkey && code == readiness.PermNeedsBinding
	switch {
	case binding:
		why = i18n.T(l, "wiz.perm.binding")
	case have && code != "" && !readiness.PermissionOK(code):
		why = i18n.Tf(l, "wiz.result.fix", fixText(l, code))
	}
	title := w.text(medium(label(th, 14, i18n.T(l, "wiz.perm."+string(c)), th.Pal.Fg)))
	sub := w.text(kit.Muted(th, why))
	w.frame.texts = append(w.frame.texts, word)
	tileCol, bg := th.Pal.AccentText, color.NRGBA{}
	// Only a row that needs the user is tinted; a fallback already works.
	needsUser := code != "" && !readiness.PermissionOK(code)
	switch {
	case ds == theme.StatusWarn && !needsUser:
		tileCol = th.Pal.Warn
	case ds == theme.StatusWarn:
		tileCol, bg = th.Pal.Warn, soft(th, th.Pal.Warn, 0.04)
	case ds == theme.StatusError:
		tileCol, bg = th.Pal.Error, soft(th, th.Pal.Error, 0.04)
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.IconTile(gtx, th, permissionIcons[c], tileCol) }),
			kit.HSpace(th.Dp(16)),
			layout.Flexed(1, vstack(th, 2, title, sub)),
			kit.HSpace(th.Dp(16)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Badge(gtx, th, ds, word) }),
		)
	}
	var extra layout.Widget
	if binding {
		extra = p.binding(w, th, l)
	}
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				fillRect(gtx, bg)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			},
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: th.Dp(16), Bottom: th.Dp(16), Left: th.Dp(20), Right: th.Dp(20)}.Layout(gtx, vstack(th, 14, head, extra))
			})
	}
}

// binding is the shortcut to add in the system settings, with Copy. The
// text comes from the platform as is; the wizard adds no environment
// names around it.
func (p *permState) binding(w *Wizard, th *theme.Theme, l i18n.Lang) layout.Widget {
	txt := ""
	if w.cfg.Backends.Permissions != nil {
		txt = w.cfg.Backends.Permissions.HotkeyInstructions()
	}
	w.frame.texts = append(w.frame.texts, txt)
	mode := w.text(kit.Muted(th, i18n.T(l, "wiz.perm.mode")))
	copyB := kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: i18n.T(l, "wiz.perm.copy"), Icon: icon.Copy2}
	if p.copied {
		copyB.Label, copyB.Icon = i18n.T(l, "wiz.perm.copied"), icon.Check
	}
	return func(gtx layout.Context) layout.Dimensions {
		// Aligned with the row text: the 40px tile plus its 16px gap.
		return layout.Inset{Left: th.Dp(56)}.Layout(gtx, vstack(th, 8,
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return kit.CodeBlock(gtx, th, txt)
			},
			hstack(th, 8, func(gtx layout.Context) layout.Dimensions {
				return w.btn(gtx, th, ActCopy, copyB, txt != "")
			}, mode),
		))
	}
}

func (p *permState) footer(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	f := footerSpec{
		secondary: w.checkButton(th, l, s, readiness.StepPermissions, kit.Secondary, icon.Refresh2, true),
		primary:   w.next(th, l, s),
	}
	if r, ok := s.result(readiness.StepPermissions); ok && !s.running {
		n := 0
		for _, c := range permissionCaps {
			if readiness.PermissionOK(r.Detail[string(c)]) {
				n++
			}
		}
		f.status = i18n.Tf(l, "wiz.perm.ready", n, len(permissionCaps))
		if !r.At.IsZero() {
			f.status += " · " + i18n.Tf(l, "wiz.result.at", r.At.Local().Format("15:04"))
		}
	}
	return f
}
