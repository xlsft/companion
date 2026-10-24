package wizard

import (
	"context"
	"time"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
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
