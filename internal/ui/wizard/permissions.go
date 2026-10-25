package wizard

import (
	"sync/atomic"

	"github.com/xlsft/companion/internal/readiness"
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
	hotkeys *HotkeyEditor // UI goroutine only, created on first use
	// recheck: shortcuts were applied, so the check runs again.
	recheck atomic.Bool
}

func (p *permState) editor(w *Wizard) *HotkeyEditor {
	if p.hotkeys == nil {
		p.hotkeys = &HotkeyEditor{
			Backend:    w.cfg.Backends.Permissions,
			Invalidate: w.cfg.Invalidate,
			ApplyBtn:   w.clickable(ActApplyHotkeys),
			OnApplied: func() {
				p.recheck.Store(true)
				w.cfg.Invalidate()
			},
		}
	}
	return p.hotkeys
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
