package ui

import (
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/ui/wizard"
)

// wizardUI is the only window content allowed before the app is Ready: the
// setup wizard (internal/ui/wizard) plus Diagnostics, nothing from the main
// UI. The wizard leaves setup only through the Gate becoming Ready.
type wizardUI struct {
	sh  *Shell
	wiz *wizard.Wizard

	mu        sync.Mutex // guards diagShown; leaf lock like mainUI.mu
	diagShown bool

	backBtn widget.Clickable
	diag    *diagPage
}

func newWizardUI(sh *Shell) *wizardUI {
	w := &wizardUI{sh: sh}
	w.wiz = wizard.New(wizard.Config{
		Gate:     sh.opts.Gate,
		Backends: sh.opts.Setup,
		Diagnose: sh.opts.Diagnose,
		SpeechLanguage: func() string {
			return sh.opts.Settings.Get().Voice.Language
		},
		OnReady: func() {
			// EnterMain closes the wizard window; never on its goroutine.
			go func() {
				if err := sh.EnterMain(); err != nil {
					sh.log.Warn("enter main mode failed", "err", err)
				}
			}()
		},
		OnDiagnostics: func() { w.showDiagnostics(true); sh.invalidateAll() },
		OnQuit:        sh.Quit,
		Invalidate:    sh.invalidateAll,
		Log:           sh.log,
	})
	return w
}

// close cancels the wizard's running checks, downloads and meter.
func (w *wizardUI) close() { w.wiz.Close() }

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
	if w.backBtn.Clicked(gtx) {
		w.showDiagnostics(false)
	}
	if !w.diagnosticsShown() {
		return w.wiz.Layout(gtx, th, l)
	}
	// Diagnostics is read-only here: it offers no way out of setup
	// (FR-ONB-014), only back to the wizard.
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
