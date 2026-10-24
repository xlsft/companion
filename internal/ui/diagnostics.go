package ui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/openclick/internal/ui/theme"
	"github.com/xlsft/openclick/pkg/contracts"
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

// statusOf maps a capability status to its display status and label key.
func statusOf(s contracts.Status) (theme.Status, string) {
	switch s {
	case contracts.StatusAvailable:
		return theme.StatusOK, "status.available"
	case contracts.StatusDegraded:
		return theme.StatusWarn, "status.degraded"
	case contracts.StatusNeedsPermission:
		return theme.StatusWarn, "status.needs_permission"
	case contracts.StatusUnavailable:
		return theme.StatusError, "status.unavailable"
	}
	return theme.StatusUnknown, "status.unknown"
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
	caps := make([]contracts.Capability, 0, len(report.Capabilities))
	for c := range report.Capabilities {
		caps = append(caps, c)
	}
	slices.Sort(caps)

	var rows []layout.Widget
	rows = append(rows,
		heading(th, T(l, "diag.heading")).Layout,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 12, Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return button(gtx, th, &p.runBtn, T(l, "diag.run"))
			})
		},
	)
	if running {
		rows = append(rows, muted(th, T(l, "diag.running")).Layout)
	}
	if err != nil {
		rows = append(rows, th.StatusLabel(theme.StatusWarn, T(l, "diag.failed")).Layout)
	}
	if have {
		rows = append(rows, body(th, Tf(l, "diag.system", strings.TrimSpace(report.OS+" "+report.OSVersion), report.Session, orDash(report.Desktop))).Layout)
	}
	for _, c := range caps {
		st := report.Capabilities[c]
		ds, key := statusOf(st.Status)
		txt := string(c) + " — " + T(l, key)
		if st.Backend != "" {
			txt += " (" + st.Backend + ")"
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(th.StatusLabel(ds, txt).Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if st.Reason == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Left: 20}.Layout(gtx, muted(th, st.Reason).Layout)
					}),
				)
			})
		})
	}
	return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(th.Theme, &p.list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			return rows[i](gtx)
		})
	})
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
