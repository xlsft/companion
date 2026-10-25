package ui

import (
	"context"
	"sync"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
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
	diag    *diagView
	list    widget.List
	update  *updateChip
}

// newWizardUI runs with sh.mu held.
func newWizardUI(sh *Shell) *wizardUI {
	w := &wizardUI{sh: sh, update: newUpdateChip(sh.opts.Updates)}
	w.wiz = wizard.New(wizard.Config{
		Gate:         sh.opts.Gate,
		Backends:     sh.opts.Setup,
		Diagnose:     sh.opts.Diagnose,
		StartOnVoice: sh.revoice,
		SpeechLanguage: func() string {
			s := sh.opts.Settings.Get()
			return hw.SpeechLanguage(s.Voice.Language, s.UI.Language)
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
		SetLanguage: func(l Lang) {
			ctx, cancel := context.WithTimeout(context.Background(), saveTimout)
			defer cancel()
			if err := sh.opts.Settings.Update(ctx, func(s *settings.Settings) { s.UI.Language = string(l) }); err != nil {
				sh.log.Warn("language change failed", "err", err)
			}
		},
		Version: sh.opts.Version,
		AsideExtra: func(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
			return w.update.Layout(gtx, th, l)
		},
		Invalidate: sh.invalidateAll,
		Log:        sh.log,
	})
	w.list.Axis = layout.Vertical
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
	return w.content(gtx, th, l)
}

func (w *wizardUI) content(gtx layout.Context, th *theme.Theme, l Lang) layout.Dimensions {
	if w.backBtn.Clicked(gtx) {
		w.showDiagnostics(false)
	}
	if !w.diagnosticsShown() {
		return w.wiz.Layout(gtx, th, l)
	}
	// Diagnostics is read-only here: it offers no way out of setup
	// (FR-ONB-014), only back to the wizard. It is the same view as
	// Settings › Diagnostics.
	if w.diag == nil {
		w.diag = newDiagView(w.sh.diag, w.sh.opts.Version)
	}
	s := w.diag.update(gtx)
	back := func(gtx layout.Context) layout.Dimensions {
		return kit.ButtonStyle{Kind: kit.Ghost, Small: true, Icon: icon.Left, Label: T(l, "wizard.back")}.Layout(gtx, th, &w.backBtn)
	}
	rows := append([]layout.Widget{kit.Title(th, T(l, "diag.heading")).Layout}, w.diag.rows(th, l, s)...)
	return pageScaffold(gtx, th, back, w.diag.actions(th, l, s), &w.list, rows)
}
