package ui

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/dictation"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// DictationBackend is raw system dictation (*dictation.Service and the
// chord that drives it).
type DictationBackend interface {
	State() dictation.State
	Cancel() bool
	Dismiss()
	// Watch calls fn after every state change until cancelled.
	Watch(fn func()) (cancel func())
	// Shortcut is the dictation chord for running text, "" without one.
	Shortcut() string
}

// dictationBanner is the notice across the main window while a dictation
// runs, and after one that typed nothing, until the user dismisses it.
type dictationBanner struct {
	be              DictationBackend
	cancel, dismiss widget.Clickable
}

func newDictationBanner(be DictationBackend) *dictationBanner { return &dictationBanner{be: be} }

// wrap puts the banner, when there is one, above content.
func (b *dictationBanner) wrap(gtx layout.Context, th *theme.Theme, l Lang, content layout.Widget) layout.Dimensions {
	gtx.Constraints.Min = gtx.Constraints.Max
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return b.Layout(gtx, th, l) }),
		layout.Flexed(1, content),
	)
}

func (b *dictationBanner) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if b == nil || b.be == nil {
		return layout.Dimensions{}
	}
	if b.cancel.Clicked(gtx) {
		b.be.Cancel()
	}
	if b.dismiss.Clicked(gtx) {
		b.be.Dismiss()
	}
	st := b.be.State()
	bn, ok := dictationBannerFor(l, st, b.be.Shortcut())
	if !ok {
		return layout.Dimensions{}
	}
	if st.Phase == dictation.Idle {
		bn.Actions = []layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "dictation.dismiss")}.Layout(gtx, th, &b.dismiss)
		}}
	} else {
		bn.Actions = []layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return kit.ButtonStyle{Kind: kit.Secondary, Small: true, Label: T(l, "dictation.cancel")}.Layout(gtx, th, &b.cancel)
		}}
	}
	return bn.Layout(gtx, th)
}

// dictationBannerFor is the banner for a state; false when there is
// nothing to show.
func dictationBannerFor(l Lang, st dictation.State, chord string) (kit.Banner, bool) {
	b := kit.Banner{Progress: -1}
	switch st.Phase {
	case dictation.Listening:
		b.Icon, b.Title = icon.Mic, T(l, "dictation.listening")
		b.Detail = T(l, "dictation.listening.noKey")
		if chord != "" {
			b.Detail = Tf(l, "dictation.listening.d", chord)
		}
	case dictation.Transcribing:
		b.Icon, b.Title = icon.Time, T(l, "dictation.transcribing")
	case dictation.Typing:
		b.Icon, b.Title = icon.Keyboard, T(l, "dictation.typing")
	default:
		if st.Err == "" {
			return kit.Banner{}, false
		}
		b.Status, b.Icon = theme.StatusError, icon.Warning
		if st.Err == dictation.CodeNoSpeech || st.Err == dictation.CodeBusy {
			b.Status = theme.StatusWarn
		}
		b.Title = T(l, "dictation.failed")
		b.Detail = dictationReason(l, st.Err)
	}
	return b, true
}

// dictationReason words a dictation failure code.
func dictationReason(l Lang, code string) string {
	if k := "dictation.err." + code; i18n.Has(k) {
		return T(l, k)
	}
	return T(l, "dictation.err.insert_failed")
}
