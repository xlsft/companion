package ui

import (
	"context"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

const diagTimeout = 10 * time.Second

// diagModel runs capability detection off the UI goroutine and keeps the
// last report. Shared by the wizard and main windows.
type diagModel struct {
	run        func(context.Context) (contracts.CapabilityReport, error)
	invalidate func()

	mu      sync.Mutex
	running bool
	have    bool
	report  contracts.CapabilityReport
	err     error
}

func newDiagModel(run func(context.Context) (contracts.CapabilityReport, error), invalidate func()) *diagModel {
	return &diagModel{run: run, invalidate: invalidate}
}

// Refresh starts detection unless it is already running.
func (d *diagModel) Refresh() {
	d.mu.Lock()
	if d.running || d.run == nil {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), diagTimeout)
		defer cancel()
		r, err := d.run(ctx)
		d.mu.Lock()
		d.report, d.err, d.have, d.running = r, err, true, false
		d.mu.Unlock()
		d.invalidate()
	}()
}

func (d *diagModel) snapshot() (r contracts.CapabilityReport, err error, running, have bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.report, d.err, d.running, d.have
}

type diagPage struct {
	model  *diagModel
	runBtn widget.Clickable
	list   widget.List
}

func newDiagPage(m *diagModel) *diagPage {
	p := &diagPage{model: m}
	p.list.Axis = layout.Vertical
	return p
}

func (p *diagPage) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if p.runBtn.Clicked(gtx) {
		p.model.Refresh()
	}
	report, err, running, have := p.model.snapshot()
	if !have && !running {
		p.model.Refresh()
		running = true
	}
	var rows []layout.Widget
	if running {
		rows = append(rows, muted(th, T(l, "diag.running")).Layout)
		// Poll as well: an invalidate from the detection goroutine can
		// land while this very frame is being drawn and be lost, leaving
		// the page on "checking" until something else redraws it.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
	if err != nil {
		rows = append(rows, th.StatusLabel(theme.StatusWarn, T(l, "diag.failed")).Layout)
	}
	if have {
		// The session type and desktop stay in the JSON report: the page
		// names no desktop environment or display protocol.
		rows = append(rows, body(th, Tf(l, "diag.system", strings.TrimSpace(report.OS+" "+report.OSVersion))).Layout)
	}
	if have {
		rows = append(rows, kit.CapabilityTable(th, l, report, nil))
	}
	return pageScaffold(gtx, th, T(l, "diag.heading"), func(gtx layout.Context) layout.Dimensions {
		if running {
			gtx = gtx.Disabled()
		}
		return kit.ButtonStyle{Kind: kit.Secondary, Label: T(l, "diag.run"), Icon: icon.Refresh2, Small: true}.Layout(gtx, th, &p.runBtn)
	}, &p.list, rows)
}
