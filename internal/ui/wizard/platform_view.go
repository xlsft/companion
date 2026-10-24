package wizard

import (
	"slices"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

func (p *platState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	w.mu.Lock()
	running, have, report, failed := p.running, p.have, p.report, p.failed
	w.mu.Unlock()
	var rows []layout.Widget
	if running {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.platform.detecting"))))
	}
	if failed {
		rows = append(rows, alert(w, th, theme.StatusWarn, i18n.T(l, "wiz.platform.partial"), ""))
	}
	if w.cfg.Diagnose == nil {
		rows = append(rows, alert(w, th, theme.StatusError, i18n.T(l, "wiz.notWired"), ""))
	}
	if have {
		rows = append(rows, p.table(w, th, l, report))
	}
	return stepBody{rows: rows}
}

func (p *platState) table(w *Wizard, th *theme.Theme, l i18n.Lang, report contracts.CapabilityReport) layout.Widget {
	return kit.CapabilityTable(th, l, report, func(s string) { w.frame.texts = append(w.frame.texts, s) })
}

func (p *platState) footer(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	i := slices.Index(readiness.Steps, readiness.StepPlatform)
	if s.unlocked(readiness.Steps[i+1]) {
		return footerSpec{primary: w.next(th, l, s)}
	}
	// Reading the table is the step: its check confirms the report and
	// the Gate moves on when it passes.
	act := ActCheck
	if _, ok := s.result(readiness.StepPlatform); ok {
		act = ActRecheck
	}
	return footerSpec{primary: func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, act, kit.ButtonStyle{Kind: kit.Primary, Label: i18n.T(l, "wiz.platform.got")}, !s.running)
	}}
}
