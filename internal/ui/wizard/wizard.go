// Package wizard is the setup wizard (OC-70, US-00, FR-ONB-001…014): the
// only window content before the app is Ready. It renders the Readiness
// Gate's steps in order and runs each step's check through the Gate; it
// never decides an outcome itself and offers no way around a step.
//
// Allowed actions are the current step's own controls, Back, Next (only to
// a step the Gate unlocked), Re-check, Diagnostics, the interface language
// and Quit (FR-ONB-004). There is no skip, no "later", no chat.
package wizard

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// Action identifies a control the wizard offers. Tests assert that no
// bypass action ever appears (FR-ONB-004).
type Action string

const (
	ActBack         Action = "back"
	ActNext         Action = "next" // only to an unlocked step
	ActCheck        Action = "check"
	ActRecheck      Action = "recheck"
	ActDiagnostics  Action = "diagnostics"
	ActQuit         Action = "quit"
	ActSelectStep   Action = "select_step" // sidebar: an unlocked step
	ActApplyHotkeys Action = "apply_hotkeys"
	ActSave         Action = "save"
	ActTest         Action = "test" // BYOK check
	ActDownload     Action = "download"
	ActCancel       Action = "cancel_download"
	ActMicTest      Action = "test_microphone"
	ActLoadVoices   Action = "load_voices"
	ActChoose       Action = "choose" // a dropdown
	ActRunFull      Action = "run_full_test"
	ActLanguage     Action = "language"
	ActReplaceKey   Action = "replace_key" // show the key editor over a stored key
	ActHeaders      Action = "headers"     // expand the extra headers field
	ActModelList    Action = "model_list"  // back from typing a model to the fetched list
	ActRetryModel   Action = "retry_model" // the speech model's download failed
	ActVoiceCheck   Action = "voice_check" // the voice is spoken and heard
	ActReadSelf     Action = "read_self"   // the user reads the phrase instead
	ActOpen         Action = "open"        // finish setup, open the main window
	ActChangeKeys   Action = "change_keys" // show the shortcut editor
)

// Config wires the wizard. Gate is required; the callbacks may be nil.
type Config struct {
	// AsideExtra, when set, draws under the language picker at the very
	// bottom of the sidebar (the update chip).
	AsideExtra func(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions
	Gate       *readiness.Gate
	Backends   Backends
	// Diagnose produces the capability report for the platform step.
	Diagnose func(ctx context.Context) (contracts.CapabilityReport, error)
	// SpeechLanguage is the configured speech language ("auto" or a code)
	// used for the STT model recommendation.
	SpeechLanguage func() string
	// OnReady runs exactly once, off the UI goroutine, when the user opens
	// Companion from the finish of a Ready Gate (FR-ONB-002/010); the shell
	// then enters main mode.
	OnReady func()
	// StartOnVoice opens the voice screen whatever the Gate's progress:
	// the user came from Settings to change the voice, not to finish setup.
	StartOnVoice  bool
	OnDiagnostics func()
	OnQuit        func()
	// Version is shown under Diagnostics when set.
	Version string
	// SetLanguage, if set, offers the interface language switch; it runs
	// off the UI goroutine.
	SetLanguage func(i18n.Lang)
	// Invalidate requests a new frame after background work.
	Invalidate func()
	Log        *slog.Logger
}

// Wizard implements the setup window content. Layout runs on the window
// goroutine; every backend and Gate call runs on its own goroutine with a
// context that ends when the user leaves the step or the wizard is closed.
type Wizard struct {
	cfg       Config
	ctx       context.Context
	cancel    context.CancelFunc
	readyOnce sync.Once

	mu         sync.Mutex // guards the fields below and the async parts of the step states
	prog       readiness.Progress
	running    bool   // a Gate check is in flight
	runErr     string // code of the last Gate.Run error (not a check failure)
	stepCtx    context.Context
	stepCancel context.CancelFunc

	bg    background
	model speechModel

	// UI goroutine only.
	scr       screen
	started   bool // the first frame chose the screen
	entered   readiness.Step
	btns      map[Action]*widget.Clickable
	langDD    kit.Dropdown
	list      widget.List
	frame     frameRecord
	auto      map[readiness.Step]autoRecord
	keysShown bool // the shortcut editor is open on the finish

	perm permState
	byok byokState
	stt  sttState
	tts  ttsState
}

// frameRecord is what the last frame showed, for tests: no hidden bypass,
// no secret on screen.
type frameRecord struct {
	actions []Action
	texts   []string
}

// New starts following the Gate. Close releases it.
func New(cfg Config) *Wizard {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Invalidate == nil {
		cfg.Invalidate = func() {}
	}
	if cfg.SpeechLanguage == nil {
		cfg.SpeechLanguage = func() string { return "auto" }
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Wizard{cfg: cfg, ctx: ctx, cancel: cancel, btns: map[Action]*widget.Clickable{}, auto: map[readiness.Step]autoRecord{}}
	w.list.Axis = layout.Vertical
	w.prog = cfg.Gate.Progress()
	w.stepCtx, w.stepCancel = context.WithCancel(ctx)
	w.byok.init()
	w.tts.init()

	ch, unsub := cfg.Gate.Subscribe()
	go func() {
		defer unsub()
		for {
			select {
			case p := <-ch:
				w.mu.Lock()
				w.prog = p
				w.mu.Unlock()
				w.cfg.Invalidate()
			case <-ctx.Done():
				return
			}
		}
	}()
	w.startSpeechModel()
	return w
}

// Open leaves setup for the main window once the Gate is Ready: on the
// user's click, so the finish can be read first. It does nothing before.
func (w *Wizard) Open() {
	if w.snap().prog.State != readiness.Ready {
		return
	}
	go w.readyOnce.Do(func() {
		if w.cfg.OnReady != nil {
			w.cfg.OnReady()
		}
	})
}

// Close cancels all running work: checks, downloads, the level meter,
// a shortcut apply.
func (w *Wizard) Close() {
	w.cancel()
	if w.perm.hotkeys != nil {
		w.perm.hotkeys.Close()
	}
}

type snapshot struct {
	prog    readiness.Progress
	running bool
	runErr  string
}

func (w *Wizard) snap() snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return snapshot{prog: w.prog, running: w.running, runErr: w.runErr}
}

func (s snapshot) result(st readiness.Step) (readiness.Result, bool) {
	r, ok := s.prog.Results[st]
	return r, ok && r.Status != readiness.Pending
}

// goStep runs fn off the UI goroutine with the current step's context.
func (w *Wizard) goStep(fn func(ctx context.Context)) {
	w.mu.Lock()
	ctx := w.stepCtx
	w.mu.Unlock()
	go func() {
		fn(ctx)
		w.cfg.Invalidate()
	}()
}

// runCheck runs the step's Gate check; before, if set, prepares it (e.g.
// releasing the microphone) and aborts the run on error.
func (w *Wizard) runCheck(st readiness.Step, before func(ctx context.Context) error, after func(ctx context.Context)) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running, w.runErr = true, ""
	ctx := w.stepCtx
	w.mu.Unlock()
	w.cfg.Invalidate()
	go func() {
		var code string
		if before != nil {
			if err := before(ctx); err != nil {
				code = "prepare_failed"
			}
		}
		if code == "" {
			if _, err := w.cfg.Gate.Run(ctx, st); err != nil {
				code = runErrCode(err)
				w.cfg.Log.Warn("setup check did not run", "step", string(st), "code", code)
			}
		}
		if after != nil {
			after(ctx)
		}
		w.mu.Lock()
		w.running, w.runErr = false, code
		w.mu.Unlock()
		w.cfg.Invalidate()
	}()
}

func runErrCode(err error) string {
	switch {
	case errors.Is(err, readiness.ErrStepLocked):
		return "step_locked"
	case errors.Is(err, readiness.ErrUnknown):
		return "unknown_step"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "store_error"
}

// autoRecord remembers the last automatic run of a step in this session.
type autoRecord struct {
	sig string
	at  time.Time
}

// autoCheck starts the screen's check without a click (the owner wants
// "does it work" answered on arrival): the agent once its form is saved,
// the voice check once the speech model is ready, the finish once every
// other step passed. Each runs again only when what it covers changed.
func (w *Wizard) autoCheck(gtx layout.Context, s snapshot) {
	if s.running {
		return
	}
	var st readiness.Step
	var sig string
	switch w.scr {
	case screenAgent:
		var ok bool
		if sig, ok = w.byok.autoSig(w); !ok {
			return
		}
		st = readiness.StepBYOK
	case screenVoice:
		var ok bool
		if sig, ok = w.voiceSig(); !ok {
			return
		}
		st = readiness.StepTTS
	case screenDone:
		for _, other := range readiness.Steps[:len(readiness.Steps)-1] {
			if r, ok := s.result(other); !ok || r.Status != readiness.Passed {
				return
			}
		}
		st = readiness.StepEndToEnd
	default:
		return
	}
	if prev, ran := w.auto[st]; ran && prev.sig == sig {
		return
	}
	w.auto[st] = autoRecord{sig: sig, at: gtx.Now}
	if st == readiness.StepTTS {
		w.voiceCheck()
		return
	}
	w.runCheck(st, nil, nil)
}
