// Package ui is the Gio application shell: the setup wizard window, the main
// window (Home, Settings, navigation), the tray menu and the window
// lifecycle. It holds no business logic; state lives in domain services such
// as internal/settings.
//
// The shell runs in one of two modes. In ModeSetup only the wizard window
// exists: Home, chats, history, Talk and agent UI are not even constructed
// (US-00 / FR-ONB-002). ModeMain builds the main UI lazily on first use.
package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/internal/ui/wizard"
	"github.com/xlsft/companion/pkg/contracts"
)

// Mode selects which windows the shell may create.
type Mode int

const (
	ModeSetup Mode = iota
	ModeMain
)

func (m Mode) String() string {
	if m == ModeMain {
		return "main"
	}
	return "setup"
}

// ParseMode parses "setup" or "main".
func ParseMode(s string) (Mode, error) {
	switch s {
	case "setup":
		return ModeSetup, nil
	case "main":
		return ModeMain, nil
	}
	return 0, fmt.Errorf("ui: unknown mode %q (want setup or main)", s)
}

// Tray menu item IDs.
const (
	TrayOpen        = "open"
	TrayDiagnostics = "diagnostics"
	TrayQuit        = "quit"
)

// TrayItems is the tray menu for a mode. Setup mode must never offer Talk or
// chat actions (FR-ONB-003); main-mode actions are added here as they land.
func TrayItems(m Mode, l Lang) []contracts.TrayMenuItem {
	open := "tray.open"
	if m == ModeSetup {
		open = "tray.open.setup"
	}
	return []contracts.TrayMenuItem{
		{ID: TrayOpen, Label: T(l, open)},
		{ID: TrayDiagnostics, Label: T(l, "tray.diagnostics")},
		{ID: TrayQuit, Label: T(l, "tray.quit")},
	}
}

// SettingsStore is the part of *settings.Service the UI uses.
type SettingsStore interface {
	Get() settings.Settings
	Update(ctx context.Context, fn func(*settings.Settings)) error
	Subscribe() (<-chan settings.Settings, func())
}

// Options configure a Shell. Settings is required, Gate too in setup mode.
type Options struct {
	Mode     Mode
	Settings SettingsStore
	// Gate decides readiness; the wizard runs its checks and the shell
	// enters main mode once it is Ready (OC-70).
	Gate *readiness.Gate
	// Setup are the wizard's step backends; nil ones leave their step's
	// controls disabled.
	Setup wizard.Backends
	// Tray may be nil; then closing the last window quits the app.
	Tray contracts.Tray
	// Chat is the Home chat's backend; nil shows the chat as not connected.
	Chat ChatBackend
	// Guide runs walkthroughs; nil shows no walkthrough card.
	Guide GuideBackend
	// Updates is the in-app updater; nil shows no update banner and leaves
	// the update settings without a status.
	Updates UpdateBackend
	// Memory is the remembered-facts store; nil leaves the Memory settings
	// with only the on/off switch.
	Memory MemoryBackend
	// Dictation is raw system dictation; nil shows no dictation banner.
	Dictation DictationBackend
	// ChangeVoice leads to the voice choice from Settings; nil hides the
	// button next to the current voice. With a Gate it defaults to
	// Shell.ChangeVoice, the setup's voice screen.
	ChangeVoice func()
	// Diagnose produces the capability report for Diagnostics.
	Diagnose func(ctx context.Context) (contracts.CapabilityReport, error)
	// Quit asks the process to shut down; the app lifecycle then calls Stop.
	Quit func()
	Log  *slog.Logger
	// Appearance supplies the system colour scheme and its changes; nil
	// means light. The caller starts it.
	Appearance contracts.AppearanceSource
	Version    string
}

// Shell implements app.Component.
type Shell struct {
	opts      Options
	log       *slog.Logger
	newWindow windowFactory
	diag      *diagModel
	quitOnce  sync.Once
	stopOnce  sync.Once
	done      chan struct{} // closed by Stop

	mu       sync.Mutex
	mode     Mode
	started  bool
	stopping bool
	trayOK   bool
	lang     Lang
	dark     bool
	scale    float32
	wizard   *wizardUI // setup mode only
	// revoice is setup reopened from Settings to change the voice: the
	// wizard starts on its voice screen and the main UI is kept.
	revoice bool
	main    *mainUI // nil until first needed in main mode
	wizWin  window
	mainWin window
	closing map[window]bool // closed by the shell, not the user
	unsub   func()
	unwatch func() // stops update state notifications
	undict  func() // stops dictation state notifications
	// mainBuilds counts main UI constructions; tests assert laziness.
	mainBuilds int
}

// New returns a shell that opens real Gio windows.
func New(opts Options) *Shell { return newShell(opts, newGioWindow) }

func newShell(opts Options, f windowFactory) *Shell {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Quit == nil {
		opts.Quit = func() {}
	}
	s := &Shell{
		opts:      opts,
		log:       opts.Log,
		newWindow: f,
		mode:      opts.Mode,
		scale:     1,
		closing:   map[window]bool{},
		done:      make(chan struct{}),
	}
	if opts.Appearance != nil {
		s.dark = opts.Appearance.Dark()
	}
	s.diag = newDiagModel(opts.Diagnose, s.invalidateAll)
	if s.opts.ChangeVoice == nil && opts.Gate != nil {
		// The click comes from the main window, which ChangeVoice closes.
		s.opts.ChangeVoice = func() {
			go func() {
				if err := s.ChangeVoice(); err != nil {
					s.log.Warn("change voice: setup not reopened", "err", err)
				}
			}()
		}
	}
	return s
}

func (s *Shell) Name() string { return "ui" }

// Start brings up the tray and the first window of the current mode.
func (s *Shell) Start(ctx context.Context) error {
	if s.opts.Settings == nil {
		return errors.New("ui: Options.Settings is required")
	}
	if s.opts.Mode == ModeSetup && s.opts.Gate == nil {
		return errors.New("ui: Options.Gate is required in setup mode")
	}
	lang := langOf(s.opts.Settings.Get().UI.Language)
	ch, unsub := s.opts.Settings.Subscribe()
	s.mu.Lock()
	s.lang = lang
	s.unsub = unsub
	mode := s.mode
	s.mu.Unlock()
	go s.watchSettings(ch)
	if s.opts.Appearance != nil {
		go s.watchAppearance(s.opts.Appearance)
	}

	if s.opts.Tray != nil {
		err := s.opts.Tray.Start(ctx, T(lang, "tray.tooltip"), TrayItems(mode, lang), s.onTray)
		switch {
		case err == nil:
			s.mu.Lock()
			s.trayOK = true
			s.mu.Unlock()
		case errors.Is(err, contracts.ErrUnsupported):
			s.log.Warn("tray unavailable, closing the last window will quit", "err", err)
		default:
			s.log.Warn("tray failed to start, closing the last window will quit", "err", err)
		}
	}
	if s.opts.Updates != nil {
		unwatch := s.opts.Updates.Watch(s.invalidateAll)
		s.mu.Lock()
		s.unwatch = unwatch
		s.mu.Unlock()
	}
	if s.opts.Dictation != nil {
		undict := s.opts.Dictation.Watch(s.invalidateAll)
		s.mu.Lock()
		s.undict = undict
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	s.Open()
	return nil
}

// Stop closes all windows and the tray.
func (s *Shell) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	wins := []window{s.wizWin, s.mainWin}
	s.wizWin, s.mainWin = nil, nil
	wizUI, mainView := s.wizard, s.main
	unsub, unwatch, undict := s.unsub, s.unwatch, s.undict
	trayOK := s.trayOK
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.done) })
	if unwatch != nil {
		unwatch()
	}
	if undict != nil {
		undict()
	}
	for _, w := range wins {
		if w != nil {
			w.Close()
		}
	}
	if wizUI != nil {
		wizUI.close()
	}
	if mainView != nil {
		mainView.close()
	}
	if unsub != nil {
		unsub()
	}
	if trayOK {
		return s.opts.Tray.Stop()
	}
	return nil
}

// Mode returns the current mode.
func (s *Shell) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// TrayAvailable reports whether the tray started; without it the app quits
// when its last window closes.
func (s *Shell) TrayAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trayOK
}

// EnterMain leaves setup mode: the wizard window closes and is dropped, the
// main UI is built once and its window opened. Calling it in main mode is a
// no-op.
func (s *Shell) EnterMain() error {
	s.mu.Lock()
	if s.mode == ModeMain {
		s.mu.Unlock()
		return nil
	}
	if s.stopping {
		s.mu.Unlock()
		return errors.New("ui: shell is stopping")
	}
	s.mode, s.revoice = ModeMain, false
	wiz, wizUI := s.wizWin, s.wizard
	s.wizWin = nil
	s.wizard = nil
	if wiz != nil {
		s.closing[wiz] = true
	}
	started, trayOK, lang := s.started, s.trayOK, s.lang
	s.mu.Unlock()

	if wizUI != nil {
		wizUI.close()
	}
	if wiz != nil {
		wiz.Close()
	}
	if trayOK {
		if err := s.opts.Tray.SetItems(TrayItems(ModeMain, lang)); err != nil {
			s.log.Warn("tray menu update failed", "err", err)
		}
	}
	if started {
		s.Open()
	}
	return nil
}

// ChangeVoice goes back to setup on its voice screen, where the voice and
// the microphone are chosen and checked; the user returns through the
// finish's Open Companion, which needs a Ready Gate as on first run. The
// main UI is kept for that return. It must not run on the main window's
// goroutine, as it closes that window.
func (s *Shell) ChangeVoice() error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return errors.New("ui: shell is stopping")
	}
	if s.opts.Gate == nil {
		s.mu.Unlock()
		return errors.New("ui: changing the voice needs Options.Gate")
	}
	if s.mode == ModeSetup {
		s.mu.Unlock()
		s.Open()
		return nil
	}
	s.mode, s.revoice = ModeSetup, true
	win := s.mainWin
	s.mainWin = nil
	if win != nil {
		s.closing[win] = true
	}
	started, trayOK, lang := s.started, s.trayOK, s.lang
	s.mu.Unlock()

	if win != nil {
		win.Close()
	}
	if trayOK {
		if err := s.opts.Tray.SetItems(TrayItems(ModeSetup, lang)); err != nil {
			s.log.Warn("tray menu update failed", "err", err)
		}
	}
	if started {
		s.Open()
	}
	return nil
}

// Open shows the primary window of the current mode, re-creating it if the
// user closed it, or raising it if it is open.
func (s *Shell) Open() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopping {
		return
	}
	if s.mode == ModeSetup {
		if s.wizWin != nil {
			s.wizWin.Raise()
			return
		}
		if s.wizard == nil {
			s.wizard = newWizardUI(s)
		}
		s.wizWin = s.newWindow(windowSpec{
			Title: T(s.lang, "wizard.title"), Width: 900, Height: 680,
			Content: s.wizard, Shell: s, OnClosed: s.windowClosed,
		})
		return
	}
	if s.mainWin != nil {
		s.mainWin.Raise()
		return
	}
	s.ensureMainLocked()
	s.mainWin = s.newWindow(windowSpec{
		Title: T(s.lang, "app.name"), Width: 1080, Height: 720,
		Content: s.main, Shell: s, OnClosed: s.windowClosed,
	})
}

func (s *Shell) ensureMainLocked() {
	if s.main == nil {
		s.main = newMainUI(s)
		s.mainBuilds++
	}
}

// OpenDiagnostics shows Diagnostics in the current mode's window: the
// wizard's own view in setup mode, Settings › Diagnostics in main mode.
func (s *Shell) OpenDiagnostics() {
	s.mu.Lock()
	switch {
	case s.mode == ModeSetup:
		if s.wizard == nil {
			s.wizard = newWizardUI(s)
		}
		s.wizard.showDiagnostics(true)
	default:
		s.ensureMainLocked()
		s.main.showSettings(secDiagnostics)
	}
	s.mu.Unlock()
	s.Open()
	s.invalidateAll()
}

// ShowSettings opens Settings at the named section ("general", "voice",
// "screen", "privacy", "diagnostics") in main mode and reports whether
// the name is known.
func (s *Shell) ShowSettings(section string) bool {
	sec, ok := sectionByName(section)
	if !ok {
		return false
	}
	s.mu.Lock()
	m := s.main
	if s.mode != ModeMain {
		m = nil
	}
	s.mu.Unlock()
	if m != nil {
		m.showSettings(sec)
		s.invalidateAll()
	}
	return true
}

// Quit asks the process to shut down. Safe to call repeatedly.
func (s *Shell) Quit() {
	s.quitOnce.Do(s.opts.Quit)
}

func (s *Shell) onTray(id string) {
	switch id {
	case TrayOpen:
		s.Open()
	case TrayDiagnostics:
		s.OpenDiagnostics()
	case TrayQuit:
		s.Quit()
	}
}

// windowClosed runs on the window's goroutine after it was destroyed.
func (s *Shell) windowClosed(w window) {
	s.mu.Lock()
	byShell := s.closing[w]
	delete(s.closing, w)
	var wizUI *wizardUI
	if s.wizWin == w {
		s.wizWin = nil
		// Closing the window cancels the wizard's work; reopening builds a
		// fresh one from the Gate's persisted progress.
		wizUI, s.wizard = s.wizard, nil
	}
	if s.mainWin == w {
		s.mainWin = nil
	}
	quit := !byShell && !s.stopping && !s.trayOK && s.wizWin == nil && s.mainWin == nil
	s.mu.Unlock()
	if wizUI != nil {
		wizUI.close()
	}
	if quit {
		s.Quit()
	}
}

// watchAppearance re-applies the theme when the system scheme flips; the
// windows read it on every frame, so repainting is all that is needed.
func (s *Shell) watchAppearance(src contracts.AppearanceSource) {
	for {
		select {
		case <-src.Changes():
			s.mu.Lock()
			s.dark = src.Dark()
			s.mu.Unlock()
			s.invalidateAll()
		case <-s.done:
			return
		}
	}
}

func (s *Shell) watchSettings(ch <-chan settings.Settings) {
	for st := range ch {
		lang := langOf(st.UI.Language)
		s.mu.Lock()
		changed := lang != s.lang
		s.lang = lang
		mode, trayOK := s.mode, s.trayOK
		s.mu.Unlock()
		if changed && trayOK {
			if err := s.opts.Tray.SetItems(TrayItems(mode, lang)); err != nil {
				s.log.Warn("tray menu update failed", "err", err)
			}
		}
		s.invalidateAll()
	}
}

func (s *Shell) invalidateAll() {
	s.mu.Lock()
	wins := []window{s.wizWin, s.mainWin}
	s.mu.Unlock()
	for _, w := range wins {
		if w != nil {
			w.Invalidate()
		}
	}
}

// look returns the per-frame appearance shared by all windows.
func (s *Shell) look() (Lang, bool, float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lang, s.dark, s.scale
}

// stepScale changes the text scale by n steps; 0 resets it.
func (s *Shell) stepScale(n int) {
	s.mu.Lock()
	if n == 0 {
		s.scale = 1
	} else {
		s.scale = theme.ClampScale(s.scale + float32(n)*theme.ScaleStep)
	}
	s.mu.Unlock()
	s.invalidateAll()
}

// navShortcut handles Ctrl+1…6; only the main window has sections.
func (s *Shell) navShortcut(digit int) {
	r, ok := RouteForShortcut(digit)
	if !ok {
		return
	}
	s.Navigate(r)
}

// Navigate shows section r of the main window; it does nothing in setup
// mode, where there are no sections.
func (s *Shell) Navigate(r Route) {
	s.mu.Lock()
	m := s.main
	if s.mode != ModeMain {
		m = nil
	}
	s.mu.Unlock()
	if m == nil {
		return
	}
	m.navigate(r)
	s.invalidateAll()
}

// newChatShortcut handles Ctrl+N in the main window.
func (s *Shell) newChatShortcut() {
	s.mu.Lock()
	m := s.main
	if s.mode != ModeMain {
		m = nil
	}
	s.mu.Unlock()
	if m == nil || m.chat.be == nil {
		return
	}
	m.newChat()
	s.invalidateAll()
}
