package ui

import (
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/openclick/internal/ui/theme"
)

// wizardUI is the only window content allowed before the app is Ready. The
// real steps arrive with OC-70; this placeholder keeps the window contract:
// a setup page plus Diagnostics, nothing from the main UI.
type wizardUI struct {
	sh *Shell

	mu        sync.Mutex // guards diagShown; leaf lock like mainUI.mu
	diagShown bool

	diagBtn     widget.Clickable
	backBtn     widget.Clickable
	continueBtn widget.Clickable
	diag        *diagPage
}

func newWizardUI(sh *Shell) *wizardUI { return &wizardUI{sh: sh} }

func (w *wizardUI) showDiagnostics(on bool) {
	w.mu.Lock()
	w.diagShown = on
	w.mu.Unlock()
}

func (w *wizardUI) diagnosticsShown() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.diagShown
}

func (w *wizardUI) Layout(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if w.diagBtn.Clicked(gtx) {
		w.showDiagnostics(true)
	}
	if w.backBtn.Clicked(gtx) {
		w.showDiagnostics(false)
	}
	if w.continueBtn.Clicked(gtx) {
		// EnterMain closes this window; it must not run on its goroutine.
		go func() {
			if err := w.sh.EnterMain(); err != nil {
				w.sh.log.Warn("enter main mode failed", "err", err)
			}
		}()
	}
	if w.diagnosticsShown() {
		if w.diag == nil {
			w.diag = newDiagPage(w.sh.diag)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 24, Top: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return button(gtx, th, &w.backBtn, T(l, "wizard.back"))
				})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return w.diag.Layout(gtx, th, l) }),
		)
	}
	return layout.UniformInset(32).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(heading(th, T(l, "wizard.heading")).Layout),
			vspace(12),
			layout.Rigid(body(th, T(l, "wizard.body")).Layout),
			vspace(12),
			layout.Rigid(muted(th, T(l, "wizard.placeholder")).Layout),
			vspace(24),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &w.continueBtn, T(l, "wizard.continue"))
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return button(gtx, th, &w.diagBtn, T(l, "wizard.diagnostics"))
					}),
				)
			}),
		)
	})
}
