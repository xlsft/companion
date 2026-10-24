package wizard

import (
	"image/color"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
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
		return theme.StatusUnknown, "wiz.state.todo"
	}
	return theme.StatusError, "wiz.state.failed"
}

func (p *permState) rows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	r, have := s.result(readiness.StepPermissions)
	var rows []layout.Widget
	for _, c := range permissionCaps {
		code := ""
		if have {
			code = r.Detail[string(c)]
		}
		ds, key := permStatus(code)
		word := i18n.T(l, key)
		if ds == theme.StatusError && key == "wiz.state.failed" {
			word += " (" + code + ")"
		}
		title := w.text(kit.Subheading(th, i18n.T(l, "wiz.perm."+string(c))))
		why := w.text(kit.Muted(th, i18n.T(l, "wiz.perm."+string(c)+".why")))
		status := w.text(th.StatusLabel(ds, word))
		var fix layout.Widget
		if have && !readiness.PermissionOK(code) && code != "" {
			fix = w.text(kit.Body(th, i18n.Tf(l, "wiz.result.fix", fixText(l, code))))
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return kit.Card(gtx, th, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(title), layout.Rigid(why), kit.VSpace(4), layout.Rigid(status),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if fix == nil {
								return layout.Dimensions{}
							}
							return fix(gtx)
						}),
					)
				})
			})
		})
		if c == contracts.CapGlobalHotkey && code == readiness.PermNeedsBinding {
			rows = append(rows, p.bindingRows(w, th, l)...)
		}
	}
	rows = append(rows, w.checkButton(th, l, s, readiness.StepPermissions, true))
	return rows
}

// bindingRows shows how to bind the talk key in the compositor, with Copy.
func (p *permState) bindingRows(w *Wizard, th *theme.Theme, l i18n.Lang) []layout.Widget {
	txt := ""
	if w.cfg.Backends.Permissions != nil {
		txt = w.cfg.Backends.Permissions.HotkeyInstructions()
	}
	head := w.text(kit.Body(th, i18n.T(l, "wiz.perm.binding")))
	instr := w.text(monospace(th, txt))
	var note layout.Widget
	if p.copied {
		note = w.text(kit.Muted(th, i18n.T(l, "wiz.perm.copied")))
	}
	return []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return kit.Card(gtx, th, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(head), kit.VSpace(6), layout.Rigid(instr), kit.VSpace(6),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return w.button(gtx, th, ActCopy, i18n.T(l, "wiz.perm.copy"), txt != "")
							}),
							layout.Rigid(layout.Spacer{Width: 12}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if note == nil {
									return layout.Dimensions{}
								}
								return note(gtx)
							}),
						)
					}),
				)
			})
		})
	}}
}

func fillRect(gtx layout.Context, c color.NRGBA) {
	defer clip.Rect{Max: gtx.Constraints.Min}.Push(gtx.Ops).Pop()
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}
