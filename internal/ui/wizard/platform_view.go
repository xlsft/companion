package wizard

import (
	"image/color"
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// capOrder puts what the user meets first at the top; capabilities not
// listed follow alphabetically.
var capOrder = []contracts.Capability{
	contracts.CapMicrophone, contracts.CapScreenCapture, contracts.CapOverlayClickThrough,
	contracts.CapOverlayFallbackPanel, contracts.CapGlobalHotkey, contracts.CapHotkeyRelease,
	contracts.CapInputInjection, contracts.CapAccessibility, contracts.CapTextInsertion,
	contracts.CapAudioOutput, contracts.CapSecretStore, contracts.CapTray,
}

func orderedCaps(r contracts.CapabilityReport) []contracts.Capability {
	caps := kit.SortedCapabilities(r)
	rank := func(c contracts.Capability) int {
		if i := slices.Index(capOrder, c); i >= 0 {
			return i
		}
		return len(capOrder)
	}
	slices.SortStableFunc(caps, func(a, b contracts.Capability) int { return rank(a) - rank(b) })
	return caps
}

// fallbackNote explains an unavailable or limited capability and its
// fallback, from the capability matrix (PRD §10).
func fallbackNote(l i18n.Lang, c contracts.Capability, st contracts.CapabilityState) string {
	if st.Status == contracts.StatusAvailable {
		if k := "wiz.platform.ok." + string(c); i18n.Has(k) {
			return i18n.T(l, k)
		}
		return ""
	}
	if k := "wiz.platform.fallback." + string(c); i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "wiz.platform.fallback.generic")
}

// capName is the user-facing name of a capability. Backend names and the
// probes' technical reasons are left to Diagnostics: they name system
// components the wizard does not talk about.
func capName(l i18n.Lang, c contracts.Capability) string {
	if k := "wiz.platform.cap." + string(c); i18n.Has(k) {
		return i18n.T(l, k)
	}
	s := strings.ReplaceAll(string(c), "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// capStatus is the table's status: unavailable is muted, not red, since
// Companion keeps working through the fallback.
func capStatus(th *theme.Theme, s contracts.Status) (theme.Status, string, color.NRGBA) {
	switch s {
	case contracts.StatusAvailable:
		return theme.StatusOK, "wiz.platform.status.available", th.Pal.OK
	case contracts.StatusDegraded:
		return theme.StatusWarn, "wiz.platform.status.degraded", th.Pal.Warn
	case contracts.StatusNeedsPermission:
		return theme.StatusWarn, "wiz.platform.status.needs_permission", th.Pal.Warn
	case contracts.StatusUnavailable:
		return theme.StatusError, "wiz.platform.status.unavailable", th.Pal.Muted
	}
	return theme.StatusUnknown, "wiz.platform.status.unknown", th.Pal.Subtle
}

func (p *platState) body(w *Wizard, th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	w.mu.Lock()
	running, have, report, failed := p.running, p.have, p.report, p.failed
	w.mu.Unlock()
	var rows []layout.Widget
	if running {
		rows = append(rows, w.text(kit.Muted(th, i18n.T(l, "wiz.platform.detecting"))))
	}
	if failed {
		rows = append(rows, badgeLine(w, th, theme.StatusWarn, i18n.T(l, "wiz.platform.partial")))
	}
	if w.cfg.Diagnose == nil {
		rows = append(rows, badgeLine(w, th, theme.StatusError, i18n.T(l, "wiz.notWired")))
	}
	if have {
		rows = append(rows, p.table(w, th, l, report))
	}
	return stepBody{rows: rows}
}

func (p *platState) table(w *Wizard, th *theme.Theme, l i18n.Lang, report contracts.CapabilityReport) layout.Widget {
	// Column widths of the prototype (260 / 170 / rest), shrunk with the
	// window.
	cols := func(gtx layout.Context, a, b, c layout.Widget) layout.Dimensions {
		full := gtx.Constraints.Max.X - gtx.Dp(th.Dp(32))
		w1 := min(gtx.Dp(th.Dp(260)), full*30/100)
		w2 := min(gtx.Dp(th.Dp(170)), full*22/100)
		fixed := func(px int, wd layout.Widget) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = px, px
				return wd(gtx)
			})
		}
		return layout.Inset{Left: th.Dp(16), Right: th.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, fixed(w1, a), fixed(w2, b), layout.Flexed(1, c))
		})
	}
	head := func() layout.Widget {
		a := w.text(caption(th, i18n.T(l, "wiz.platform.col.cap")))
		b := w.text(caption(th, i18n.T(l, "wiz.platform.col.state")))
		c := w.text(caption(th, i18n.T(l, "wiz.platform.col.means")))
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Background{}.Layout(gtx,
				func(gtx layout.Context) layout.Dimensions {
					fillRect(gtx, th.Pal.Surface)
					return layout.Dimensions{Size: gtx.Constraints.Min}
				},
				func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: th.Dp(10), Bottom: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return cols(gtx, a, b, c)
					})
				})
		}
	}()
	rows := []layout.Widget{head}
	for _, c := range orderedCaps(report) {
		st := report.Capabilities[c]
		ds, key, col := capStatus(th, st.Status)
		name := w.text(label(th, 14, capName(l, c), th.Pal.Fg))
		word := i18n.T(l, key)
		w.frame.texts = append(w.frame.texts, word)
		status := statusText(th, ds, word, 14, col)
		note := w.text(label(th, 13, fallbackNote(l, c, st), th.Pal.Muted))
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(6), Bottom: th.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return kit.VCenter(gtx, gtx.Dp(th.Dp(34)), func(gtx layout.Context) layout.Dimensions {
					return cols(gtx, name, status, note)
				})
			})
		})
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Rows(gtx, th, rows...)
	}
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
