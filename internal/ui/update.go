package ui

import (
	"fmt"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/update"
)

// UpdateBackend is the in-app updater (*update.Service).
type UpdateBackend interface {
	State() update.State
	CheckNow()
	Install()
	OpenNotes()
	// Watch calls fn after every state change until cancelled.
	Watch(fn func()) (cancel func())
}

// updateBanner is the update notice at the top of a window. Each window
// content owns one, so their buttons are independent widgets.
type updateBanner struct {
	be             UpdateBackend
	install, notes widget.Clickable
}

func newUpdateBanner(be UpdateBackend) *updateBanner { return &updateBanner{be: be} }

// wrap puts the banner, when there is one, above the window content.
func (b *updateBanner) wrap(gtx layout.Context, th *theme.Theme, l Lang, content layout.Widget) layout.Dimensions {
	gtx.Constraints.Min = gtx.Constraints.Max
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return b.Layout(gtx, th, l) }),
		layout.Flexed(1, content),
	)
}

func (b *updateBanner) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if b == nil || b.be == nil {
		return layout.Dimensions{}
	}
	st := b.be.State()
	// Only act on what the last frame offered: a stale click must not
	// start an install the state no longer allows.
	canInstall := st.Latest != nil && st.Managed == "" && (st.Phase == update.PhaseIdle || st.Phase == update.PhaseFailed)
	if b.install.Clicked(gtx) && canInstall {
		b.be.Install()
		st = b.be.State()
	}
	if b.notes.Clicked(gtx) {
		b.be.OpenNotes()
	}
	bn, ok := bannerFor(l, st)
	if !ok {
		return layout.Dimensions{}
	}
	notes := func(gtx layout.Context) layout.Dimensions {
		return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "update.notes")}.Layout(gtx, th, &b.notes)
	}
	upd := func(label string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return kit.ButtonStyle{Kind: kit.Primary, Small: true, Label: label}.Layout(gtx, th, &b.install)
		}
	}
	switch {
	case st.Phase == update.PhaseFailed:
		if st.Managed == "" {
			bn.Actions = append(bn.Actions, upd(T(l, "update.retry")))
		}
		bn.Actions = append(bn.Actions, notes)
	case st.Phase == update.PhaseIdle:
		if st.Managed == "" {
			bn.Actions = append(bn.Actions, upd(T(l, "update.install")))
		}
		bn.Actions = append(bn.Actions, notes)
	case st.Phase == update.PhaseRestartNeeded:
		bn.Actions = append(bn.Actions, notes)
	}
	return bn.Layout(gtx, th)
}

// bannerFor is the banner's look and text for a state; false when there
// is nothing to show.
func bannerFor(l Lang, st update.State) (kit.Banner, bool) {
	if st.Latest == nil {
		return kit.Banner{}, false
	}
	v := st.Latest.Version.String()
	b := kit.Banner{Icon: icon.Sparkles, Progress: -1}
	switch st.Phase {
	case update.PhaseIdle:
		b.Title = Tf(l, "update.available", v)
		if st.Managed != "" {
			b.Detail = T(l, "update.managed."+st.Managed)
		}
	case update.PhaseVerifying, update.PhaseDownloading, update.PhaseInstalling, update.PhaseRestarting:
		b.Icon = icon.Time
		b.Title = Tf(l, "update.progress", v)
		switch st.Phase {
		case update.PhaseVerifying:
			b.Detail = T(l, "update.verifying")
		case update.PhaseDownloading:
			b.Detail = T(l, "update.downloading")
			b.Progress = 0
			if st.Total > 0 {
				b.Progress = float32(st.Done) / float32(st.Total)
				b.Detail = Tf(l, "update.downloadingOf", megabytes(st.Done), megabytes(st.Total))
			}
		case update.PhaseInstalling:
			b.Detail = T(l, "update.installing")
		default:
			b.Detail = T(l, "update.restarting")
		}
	case update.PhaseRestartNeeded:
		b.Status, b.Icon = theme.StatusOK, icon.CheckCircle
		b.Title = Tf(l, "update.installed", v)
		b.Detail = T(l, "update.restartNeeded")
	case update.PhaseFailed:
		b.Status, b.Icon = theme.StatusError, icon.Warning
		b.Title = Tf(l, "update.failed", v)
		b.Detail = updateReason(l, st.Err)
	}
	return b, true
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
	case st.Checking:
		return T(l, "update.status.checking")
	case st.LastCheck.IsZero():
		s = T(l, "update.status.never")
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
