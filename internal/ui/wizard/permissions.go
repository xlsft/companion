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
	rs := make([]layout.Widget, 0, len(permissionCaps))
	for _, c := range permissionCaps {
		code := ""
		if have {
			code = r.Detail[string(c)]
		}
		rs = append(rs, p.row(w, th, l, c, code, have))
	}
	return stepBody{rows: []layout.Widget{rows(th, rs...)}}
}

// row is one permission as a status row. A permission that needs the user
// is tinted and, for the hotkey, carries the shortcut to add.
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
	// Only a row that needs the user is tinted; a fallback already works
	// and only shows its state in the tile.
	needsUser := code != "" && !readiness.PermissionOK(code)
	var ic color.NRGBA
	if ds == theme.StatusWarn {
		ic = th.Pal.Warn
	}
	row := kit.StatusRow{
		Icon: permissionIcons[c], IconColor: ic,
		Title: i18n.T(l, "wiz.perm."+string(c)), Caption: why,
		Status: ds, Badge: word, Tint: needsUser,
	}
	if binding {
		row.Body = p.binding(w, th, l)
	}
	return w.statusRow(th, row)
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
	return kit.VStack(th, 8,
		func(gtx layout.Context) layout.Dimensions { return kit.CodeBlock(gtx, th, txt) },
		kit.HStack(th, 8, func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActCopy, copyB, txt != "")
		}, mode),
	)
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
