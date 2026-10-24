package ui

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"gioui.org/io/clipboard"
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
	at      time.Time
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
		d.report, d.err, d.have, d.running, d.at = r, err, true, false, time.Now()
		d.mu.Unlock()
		d.invalidate()
	}()
}

type diagSnapshot struct {
	report  contracts.CapabilityReport
	err     error
	running bool
	have    bool
	at      time.Time
}

func (d *diagModel) snapshot() diagSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return diagSnapshot{d.report, d.err, d.running, d.have, d.at}
}

// diagReport is the JSON "Copy report" puts on the clipboard: everything
// the screen leaves out for support, i.e. the session and desktop, the
// backends, probe reasons and raw facts. Probes never put user content in
// a report (contracts.CapabilityState).
type diagReport struct {
	App       string                     `json:"app_version"`
	Generated time.Time                  `json:"generated_at"`
	Error     string                     `json:"detection_error,omitempty"`
	Report    contracts.CapabilityReport `json:"report"`
}

func reportJSON(version string, s diagSnapshot) []byte {
	r := diagReport{App: version, Generated: s.at.UTC(), Report: s.report}
	if s.err != nil {
		r.Error = s.err.Error()
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil // plain data, cannot fail
	}
	return b
}

// osName is the user-facing name of runtime.GOOS.
func osName(goos string) string {
	switch goos {
	case "linux":
		return "Linux"
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return goos
}

// diagCounts is how many capabilities work and how many are limited or
// missing.
func diagCounts(r contracts.CapabilityReport) (ok, attention, total int) {
	for _, st := range r.Capabilities {
		total++
		switch st.Status {
		case contracts.StatusAvailable:
			ok++
		case contracts.StatusDegraded, contracts.StatusNeedsPermission, contracts.StatusUnavailable:
			attention++
		}
	}
	return ok, attention, total
}

// diagTile is one summary tile.
type diagTile struct {
	caption, value, sub string
	status              theme.Status
}

// diagTiles are the summary above the table, in the user's words: the
// system, what works and what needs attention. The session type and
// desktop stay in the copied report.
func diagTiles(l Lang, s diagSnapshot) []diagTile {
	// The version string usually names the distribution or release, so it
	// is the value and the OS family goes under it.
	sys, sub := osName(s.report.OS), ""
	if v := strings.TrimSpace(s.report.OSVersion); v != "" {
		sys, sub = v, osName(s.report.OS)
	}
	ok, att, total := diagCounts(s.report)
	if !s.at.IsZero() {
		at := Tf(l, "diag.checkedAt", s.at.Local().Format("15:04"))
		if sub != "" {
			at = sub + " · " + at
		}
		sub = at
	}
	attStatus, attSub := theme.StatusOK, T(l, "diag.attention.none")
	if att > 0 {
		attStatus, attSub = theme.StatusWarn, T(l, "diag.attention.some")
	}
	return []diagTile{
		{T(l, "diag.tile.system"), kit.OrDash(sys), sub, theme.StatusUnknown},
		{T(l, "diag.tile.works"), Tf(l, "diag.ofTotal", ok, total), T(l, "diag.works.sub"), theme.StatusOK},
		{T(l, "diag.tile.attention"), Tf(l, "diag.count", att), attSub, attStatus},
	}
}

// diagTexts are every text the diagnostics view shows for s; tests check
// them for environment names.
func diagTexts(l Lang, s diagSnapshot) []string {
	var out []string
	for _, t := range diagTiles(l, s) {
		out = append(out, t.caption, t.value, t.sub)
	}
	return append(out, kit.CapabilityTexts(l, s.report)...)
}

// diagView is Diagnostics: a system summary, the capability table and
// Run again / Copy report. Settings shows it as a section and the setup
// window on its own.
type diagView struct {
	model   *diagModel
	version string
	runBtn  widget.Clickable
	copyBtn widget.Clickable
	copied  bool
}

func newDiagView(m *diagModel, version string) *diagView {
	return &diagView{model: m, version: version}
}

// update handles the actions and starts a first check.
func (v *diagView) update(gtx layout.Context) diagSnapshot {
	if v.runBtn.Clicked(gtx) {
		v.copied = false
		v.model.Refresh()
	}
	s := v.model.snapshot()
	if v.copyBtn.Clicked(gtx) && s.have {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(string(reportJSON(v.version, s))))})
		v.copied = true
	}
	if !s.have && !s.running {
		v.model.Refresh()
		s.running = true
	}
	if s.running {
		// Poll as well: an invalidate from the detection goroutine can
		// land while this very frame is being drawn and be lost, leaving
		// the view on "checking" until something else redraws it.
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
	}
	return s
}

// actions are Run again and Copy report, for the page header.
func (v *diagView) actions(th *theme.Theme, l Lang, s diagSnapshot) layout.Widget {
	copyB := kit.ButtonStyle{Kind: kit.Secondary, Small: true, Icon: icon.Copy2, Label: T(l, "diag.copy")}
	if v.copied {
		copyB.Icon, copyB.Label = icon.Check, T(l, "diag.copied")
	}
	run := kit.ButtonStyle{Kind: kit.Secondary, Small: true, Icon: icon.Refresh2, Label: T(l, "diag.run")}
	return func(gtx layout.Context) layout.Dimensions {
		// In a narrow header the buttons keep their icons and names (for
		// screen readers) but drop the visible words.
		compact := gtx.Constraints.Max.X < gtx.Dp(th.Dp(560))
		button := func(b kit.ButtonStyle, c *widget.Clickable, enabled bool) layout.Widget {
			return func(gtx layout.Context) layout.Dimensions {
				if !enabled {
					gtx = gtx.Disabled()
				}
				if compact {
					return kit.IconButton(gtx, th, c, b.Icon, b.Label)
				}
				return b.Layout(gtx, th, c)
			}
		}
		return kit.HStack(th, 8, button(run, &v.runBtn, !s.running), button(copyB, &v.copyBtn, s.have && !s.running))(gtx)
	}
}

// rows are the view's page rows.
func (v *diagView) rows(th *theme.Theme, l Lang, s diagSnapshot) []layout.Widget {
	rows := []layout.Widget{kit.MaxWidth(th, 640, kit.Lead(th, T(l, "diag.lead")).Layout)}
	if s.running && !s.have {
		return append(rows, kit.Muted(th, T(l, "diag.running")).Layout)
	}
	if s.err != nil {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return kit.Alert(gtx, th, theme.StatusWarn, T(l, "diag.failed"), T(l, "diag.failed.d"))
		})
	}
	tiles := diagTiles(l, s)
	cells := make([]layout.Widget, len(tiles))
	for i, t := range tiles {
		cells[i] = func(gtx layout.Context) layout.Dimensions { return diagTileLayout(gtx, th, t) }
	}
	rows = append(rows,
		kit.Flow(th, 200, 12, cells...),
		kit.CapabilityTable(th, l, s.report, nil),
		kit.Small(th, T(l, "diag.reportNote")).Layout,
	)
	return rows
}

func diagTileLayout(gtx layout.Context, th *theme.Theme, t diagTile) layout.Dimensions {
	// Grid's minimum height makes the tiles of a row equally tall.
	return kit.Panel(gtx, th, func(gtx layout.Context) layout.Dimensions {
		return kit.Pad(th, 14, 16, func(gtx layout.Context) layout.Dimensions {
			val := kit.Medium(kit.Text(th, 15, t.value, th.Pal.Fg))
			val.MaxLines = 2
			value := val.Layout
			if t.status != theme.StatusUnknown {
				value = kit.HStack(th, 6, kit.IconW(th, kit.StatusIcon(t.status), 14, th.Pal.Color(t.status)), val.Layout)
			}
			var sub layout.Widget
			if t.sub != "" {
				sub = kit.Muted(th, t.sub).Layout
			}
			return kit.VStack(th, 6, kit.Caption(th, t.caption).Layout, value, sub)(gtx)
		})(gtx)
	})
}
