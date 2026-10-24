package wizard

import (
	"context"
	"time"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

const diagTimeout = 10 * time.Second

// platState is the capability report step; guarded by Wizard.mu.
type platState struct {
	running bool
	have    bool
	report  contracts.CapabilityReport
	failed  bool
}

func (p *platState) load(w *Wizard, _ context.Context) {
	if w.cfg.Diagnose == nil {
		return
	}
	w.mu.Lock()
	if p.running {
		w.mu.Unlock()
		return
	}
	p.running = true
	w.mu.Unlock()
	w.goStep(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, diagTimeout)
		defer cancel()
		r, err := w.cfg.Diagnose(ctx)
		w.mu.Lock()
		defer w.mu.Unlock()
		p.running = false
		p.report, p.failed, p.have = r, err != nil, true
	})
}

func (p *platState) update(w *Wizard, gtx layout.Context, s snapshot) {
	if w.checkClicked(gtx) {
		// Re-detect too, so the table shows what the check saw.
		p.load(w, nil)
		w.runCheck(readiness.StepPlatform, nil, nil)
	}
}

// fallbackNote explains an unavailable or limited capability and its
// fallback, from the Linux capability matrix (PRD §10).
func fallbackNote(l i18n.Lang, c contracts.Capability, st contracts.CapabilityState) string {
	if st.Status == contracts.StatusAvailable {
		return ""
	}
	if k := "wiz.platform.fallback." + string(c); i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "wiz.platform.fallback.generic")
}

func (p *platState) rows(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	w.mu.Lock()
	running, have, report, failed := p.running, p.have, p.report, p.failed
	w.mu.Unlock()
	var rows []layout.Widget
	if running {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.platform.detecting"))))
	}
	if failed {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusWarn, i18n.T(l, "wiz.platform.partial"))))
	}
	if w.cfg.Diagnose == nil {
		rows = append(rows, w.text(th.StatusLabel(theme.StatusError, i18n.T(l, "wiz.notWired"))))
	}
	if have {
		rows = append(rows, w.text(kit.Body(th, i18n.Tf(l, "wiz.platform.system", kit.OrDash(report.OS), kit.OrDash(string(report.Session)), kit.OrDash(report.Desktop)))))
		if report.OS == "linux" {
			rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.platform.linux"))))
		}
		caps := kit.CapabilityRows(th, l, report, func(c contracts.Capability, st contracts.CapabilityState) string {
			return fallbackNote(l, c, st)
		})
		for _, c := range kit.SortedCapabilities(report) {
			st := report.Capabilities[c]
			_, key := kit.StatusOf(st.Status)
			w.frame.texts = append(w.frame.texts, string(c)+" — "+i18n.T(l, key), fallbackNote(l, c, st))
		}
		rows = append(rows, caps...)
	}
	rows = append(rows, gap(12), w.checkButton(th, l, s, readiness.StepPlatform, true))
	return rows
}
