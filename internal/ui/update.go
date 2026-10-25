package ui

import (
	"fmt"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/update"
)

// UpdateBackend is the in-app updater (*update.Service).
type UpdateBackend interface {
	State() update.State
	CheckNow()
	Install()
	// RestartNow restarts into an installed update.
	RestartNow()
	OpenNotes()
	// Watch calls fn after every state change until cancelled.
	Watch(fn func()) (cancel func())
}

// updateChip is the update notice at the bottom of a window's sidebar:
// one line, one action, never in the way of the content. Each window owns
// one, so their buttons are independent widgets.
type updateChip struct {
	be           UpdateBackend
	body, action widget.Clickable
}

func newUpdateChip(be UpdateBackend) *updateChip { return &updateChip{be: be} }

// Layout draws the chip with its sidebar margins, or nothing when there is
// no update.
func (c *updateChip) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if c == nil || c.be == nil {
		return layout.Dimensions{}
	}
	st := c.be.State()
	p, act, ok := chipFor(l, st)
	if !ok {
		return layout.Dimensions{}
	}
	// Only act on what the last frame offered: a stale click must not
	// start an install the state no longer allows.
	if c.action.Clicked(gtx) {
		switch act {
		case chipInstall:
			c.be.Install()
		case chipRestart:
			c.be.RestartNow()
		}
		st = c.be.State()
		if p, act, ok = chipFor(l, st); !ok {
			return layout.Dimensions{}
		}
	}
	if c.body.Clicked(gtx) {
		c.be.OpenNotes()
	}
	var action *widget.Clickable
	if act != chipNone {
		action = &c.action
	}
	return layout.Inset{Left: th.Dp(8), Right: th.Dp(8), Bottom: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return p.Layout(gtx, th, &c.body, action)
	})
}

type chipAction int

const (
	chipNone chipAction = iota
	chipInstall
	chipRestart
)

// chipFor is the chip for a state and what its button does; false when
// there is nothing to show.
func chipFor(l Lang, st update.State) (kit.Pill, chipAction, bool) {
	if st.Latest == nil {
		return kit.Pill{}, chipNone, false
	}
	v := st.Latest.Version.String()
	switch st.Phase {
	case update.PhaseIdle:
		p := kit.Pill{Lead: kit.LeadDot, Text: Tf(l, "update.chip.available", v)}
		if st.Managed != "" {
			return p, chipNone, true // the package manager updates it
		}
		p.Action = T(l, "update.install")
		return p, chipInstall, true
	case update.PhaseVerifying:
		return kit.Pill{Lead: kit.LeadRing, Text: T(l, "update.chip.checking")}, chipNone, true
	case update.PhaseDownloading:
		p := kit.Pill{Lead: kit.LeadRing, Text: T(l, "update.chip.updating")}
		if st.Total > 0 {
			p.Progress = float32(st.Done) / float32(st.Total)
			p.Meta = Tf(l, "update.chip.mb", megabytes(st.Done), megabytes(st.Total))
		}
		return p, chipNone, true
	case update.PhaseInstalling:
		return kit.Pill{Lead: kit.LeadRing, Progress: 1, Text: T(l, "update.chip.installing")}, chipNone, true
	case update.PhaseRestarting:
		return kit.Pill{Lead: kit.LeadRing, Progress: 1, Text: T(l, "update.restarting")}, chipNone, true
	case update.PhaseRestartNeeded:
		if !st.CanRestart {
			return kit.Pill{Lead: kit.LeadCheck, Text: T(l, "update.chip.reopen")}, chipNone, true
		}
		return kit.Pill{Lead: kit.LeadCheck, Text: Tf(l, "update.chip.ready", v), Action: T(l, "update.chip.restart")}, chipRestart, true
	case update.PhaseFailed:
		p := kit.Pill{Lead: kit.LeadWarning, Text: T(l, "update.chip.failed")}
		if st.Managed != "" {
			return p, chipNone, true
		}
		p.Action = T(l, "update.retry")
		return p, chipInstall, true
	}
	return kit.Pill{}, chipNone, false
}

// updateReason explains an update.Code to the user.
func updateReason(l Lang, code string) string {
	if k := "update.err." + code; i18n.Has(k) {
		return T(l, k)
	}
	return T(l, "update.err.error")
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }

// updateStatus is the Settings line under "Check now".
func updateStatus(l Lang, st update.State, now time.Time) string {
	var s string
	switch {
	case st.Phase == update.PhaseFailed && st.Err != "":
		// The sidebar chip only says it failed; here is why.
		return updateReason(l, st.Err)
	case st.Checking:
		return T(l, "update.status.checking")
	case st.LastCheck.IsZero():
		s = T(l, "update.status.never")
	case st.CheckErr == "" && st.Latest != nil && st.Managed != "":
		// The chip cannot say why it offers no Update button; this can.
		s = Tf(l, "update.status.available", st.Latest.Version.String()) + " — " + T(l, "update.managed."+st.Managed)
	case st.CheckErr == "" && st.Latest != nil:
		s = Tf(l, "update.status.available", st.Latest.Version.String())
	case st.CheckErr == "":
		s = T(l, "update.status.latest")
	case i18n.Has("update.status." + st.CheckErr):
		s = T(l, "update.status."+st.CheckErr)
	default:
		s = T(l, "update.status.error")
	}
	if !st.LastCheck.IsZero() {
		at := st.LastCheck.Local()
		layout := "15:04"
		if y, m, d := at.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
			layout = "2006-01-02 15:04"
		}
		s += " · " + Tf(l, "update.status.at", at.Format(layout))
	}
	return s
}
