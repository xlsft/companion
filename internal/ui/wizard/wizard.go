// Package wizard is the setup wizard (OC-70, US-00, FR-ONB-001…014): the
// only window content before the app is Ready. It renders the Readiness
// Gate's steps in order and runs each step's check through the Gate; it
// never decides an outcome itself and offers no way around a step.
//
// Allowed actions are the current step's own controls, Back, Re-check,
// Diagnostics and Quit (FR-ONB-004). There is no skip, no "later", no chat.
package wizard

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/openclick/internal/readiness"
	"github.com/xlsft/openclick/internal/ui/i18n"
	"github.com/xlsft/openclick/internal/ui/kit"
	"github.com/xlsft/openclick/internal/ui/theme"
	"github.com/xlsft/openclick/pkg/contracts"
)

// Action identifies a control the wizard offers. Tests assert that no
// bypass action ever appears (FR-ONB-004).
type Action string

const (
	ActBack        Action = "back"
	ActCheck       Action = "check"
	ActRecheck     Action = "recheck"
	ActDiagnostics Action = "diagnostics"
	ActQuit        Action = "quit"
	ActSelectStep  Action = "select_step" // sidebar: an unlocked step
	ActCopy        Action = "copy_instructions"
	ActSave        Action = "save"
	ActTest        Action = "test" // BYOK check
	ActDownload    Action = "download"
	ActCancel      Action = "cancel_download"
	ActMicTest     Action = "test_microphone"
	ActLoadVoices  Action = "load_voices"
	ActChoose      Action = "choose" // a dropdown
	ActRunFull     Action = "run_full_test"
)

// Config wires the wizard. Gate is required; the callbacks may be nil.
type Config struct {
	Gate     *readiness.Gate
	Backends Backends
	// Diagnose produces the capability report for the platform step.
	Diagnose func(ctx context.Context) (contracts.CapabilityReport, error)
	// SpeechLanguage is the configured speech language ("auto" or a code)
	// used for the STT model recommendation.
	SpeechLanguage func() string
	// OnReady runs exactly once, off the UI goroutine, when the Gate
	// becomes Ready (FR-ONB-002/010); the shell then enters main mode.
	OnReady       func()
	OnDiagnostics func()
	OnQuit        func()
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

	// UI goroutine only.
	entered  readiness.Step
	btns     map[Action]*widget.Clickable
	stepBtns [6]widget.Clickable
	list     widget.List
	frame    frameRecord

	perm permState
	byok byokState
	stt  sttState
	tts  ttsState
	plat platState
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
	w := &Wizard{cfg: cfg, ctx: ctx, cancel: cancel, btns: map[Action]*widget.Clickable{}}
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
	ready := cfg.Gate.ReadyCh()
	go func() {
		select {
		case <-ready:
			w.readyOnce.Do(func() {
				if w.cfg.OnReady != nil {
					w.cfg.OnReady()
				}
			})
		case <-ctx.Done():
		}
	}()
	return w
}

// Close cancels all running work: checks, downloads, the level meter.
func (w *Wizard) Close() { w.cancel() }

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

// view is the step on screen: the Gate's current step.
func (s snapshot) view() readiness.Step { return s.prog.Current }

func (s snapshot) result(st readiness.Step) (readiness.Result, bool) {
	r, ok := s.prog.Results[st]
	return r, ok && r.Status != readiness.Pending
}

// unlocked reports whether every step before st passed.
func (s snapshot) unlocked(st readiness.Step) bool {
	for _, prev := range readiness.Steps[:slices.Index(readiness.Steps, st)] {
		if s.prog.Results[prev].Status != readiness.Passed {
			return false
		}
	}
	return true
}

// enterStep runs when the step on screen changes: the previous step's work
// is cancelled and the new step loads its data.
func (w *Wizard) enterStep(st readiness.Step) {
	w.entered = st
	w.mu.Lock()
	w.stepCancel()
	w.stepCtx, w.stepCancel = context.WithCancel(w.ctx)
	ctx := w.stepCtx
	w.runErr = ""
	w.mu.Unlock()
	switch st {
	case readiness.StepBYOK:
		w.byok.load(w, ctx)
	case readiness.StepSTT:
		w.stt.load(w, ctx)
	case readiness.StepTTS:
		w.tts.load(w, ctx)
	case readiness.StepPlatform:
		w.plat.load(w, ctx)
	}
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

// selectStep shows st if it is unlocked; the Gate keeps results.
func (w *Wizard) selectStep(s snapshot, st readiness.Step) {
	if !s.unlocked(st) || s.running || st == s.view() {
		return
	}
	// Show it at once; the Gate's publish confirms it.
	w.mu.Lock()
	w.prog.Current = st
	w.mu.Unlock()
	go func() {
		if err := w.cfg.Gate.Select(w.ctx, st); err != nil {
			w.cfg.Log.Warn("setup step select failed", "step", string(st), "code", runErrCode(err))
		}
		w.cfg.Invalidate()
	}()
}

func (w *Wizard) clickable(a Action) *widget.Clickable {
	b, ok := w.btns[a]
	if !ok {
		b = new(widget.Clickable)
		w.btns[a] = b
	}
	return b
}

// clicked reports a click on the action's button since the last frame.
func (w *Wizard) clicked(gtx layout.Context, a Action) bool {
	b, ok := w.btns[a]
	return ok && b.Clicked(gtx)
}

// button lays out an action button and records it for the frame.
func (w *Wizard) button(gtx layout.Context, th *theme.Theme, a Action, label string, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, a)
	w.frame.texts = append(w.frame.texts, label)
	if !enabled {
		gtx = gtx.Disabled()
	}
	return kit.Button(gtx, th, w.clickable(a), label)
}

// text records a label for the frame and returns its widget.
func (w *Wizard) text(l material.LabelStyle) layout.Widget {
	w.frame.texts = append(w.frame.texts, l.Text)
	return l.Layout
}

// Layout draws the wizard: step list, current step, action bar.
func (w *Wizard) Layout(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	w.frame = frameRecord{}
	s := w.snap()
	if s.view() != w.entered {
		w.enterStep(s.view())
	}
	w.update(gtx, s, l)
	s = w.snap()

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.sidebar(gtx, th, l, s) }),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return w.content(gtx, th, l, s) }),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.actionBar(gtx, th, l, s) }),
	)
}

// update handles the clicks of the previous frame.
func (w *Wizard) update(gtx layout.Context, s snapshot, l i18n.Lang) {
	for i, st := range readiness.Steps {
		if w.stepBtns[i].Clicked(gtx) {
			w.selectStep(s, st)
		}
	}
	if w.clicked(gtx, ActBack) {
		if i := slices.Index(readiness.Steps, s.view()); i > 0 {
			w.selectStep(s, readiness.Steps[i-1])
		}
	}
	if w.clicked(gtx, ActDiagnostics) && w.cfg.OnDiagnostics != nil {
		w.cfg.OnDiagnostics()
	}
	if w.clicked(gtx, ActQuit) && w.cfg.OnQuit != nil {
		go w.cfg.OnQuit()
	}
	switch s.view() {
	case readiness.StepPermissions:
		w.perm.update(w, gtx, s)
	case readiness.StepBYOK:
		w.byok.update(w, gtx, s)
	case readiness.StepSTT:
		w.stt.update(w, gtx, s, l)
	case readiness.StepTTS:
		w.tts.update(w, gtx, s)
	case readiness.StepPlatform:
		w.plat.update(w, gtx, s)
	case readiness.StepEndToEnd:
		if w.clicked(gtx, ActRunFull) {
			w.runCheck(readiness.StepEndToEnd, nil, nil)
		}
	}
}

func (w *Wizard) sidebar(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	width := min(gtx.Dp(unit.Dp(210*th.Scale)), gtx.Constraints.Max.X*2/5)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = width, width
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	return layout.Background{}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			fillRect(gtx, th.Pal.Sidebar)
			return layout.Dimensions{Size: gtx.Constraints.Min}
		},
		func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(w.text(kit.Subheading(th, i18n.T(l, "wiz.title")))),
				kit.VSpace(12),
			}
			for i, st := range readiness.Steps {
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return w.stepEntry(gtx, th, l, s, i, st)
				}))
			}
			return layout.UniformInset(12).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		})
}

// stepStatus is the sidebar state of a step: symbol, word, colour.
func stepStatus(s snapshot, st readiness.Step) (theme.Status, string) {
	if !s.unlocked(st) {
		return theme.StatusUnknown, "wiz.state.locked"
	}
	if s.running && st == s.view() {
		return theme.StatusWarn, "wiz.state.checking"
	}
	r, ok := s.result(st)
	switch {
	case ok && r.Status == readiness.Passed:
		return theme.StatusOK, "wiz.state.passed"
	case ok && r.Status == readiness.Failed:
		return theme.StatusError, "wiz.state.failed"
	}
	return theme.StatusUnknown, "wiz.state.todo"
}

func (w *Wizard) stepEntry(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot, i int, st readiness.Step) layout.Dimensions {
	ds, key := stepStatus(s, st)
	unlocked := s.unlocked(st)
	if unlocked {
		w.frame.actions = append(w.frame.actions, ActSelectStep)
	}
	title := i18n.Tf(l, "wiz.stepN", i+1, i18n.T(l, "wiz.step."+string(st)))
	if st == s.view() {
		title = theme.SelectedMarker + " " + title
	}
	status := ds.Symbol() + " " + i18n.T(l, key)
	w.frame.texts = append(w.frame.texts, title, status)
	if !unlocked {
		gtx = gtx.Disabled()
	}
	b := &w.stepBtns[i]
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return kit.FocusRing(gtx, th, gtx.Focused(b), func(gtx layout.Context) layout.Dimensions {
			return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.UniformInset(6).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					t := kit.Body(th, title)
					if !unlocked {
						t.Color = th.Pal.Muted
					}
					st := material.Label(th.Theme, th.Sp(0.85), status)
					st.Color = th.Pal.Color(ds)
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(t.Layout),
						layout.Rigid(st.Layout),
					)
				})
			})
		})
	})
}

func (w *Wizard) content(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	st := s.view()
	i := slices.Index(readiness.Steps, st)
	rows := []layout.Widget{
		w.text(kit.Heading(th, i18n.Tf(l, "wiz.stepOf", i+1, len(readiness.Steps), i18n.T(l, "wiz.step."+string(st))))),
		gap(8),
		w.text(kit.Body(th, i18n.T(l, "wiz.step."+string(st)+".intro"))),
		gap(16),
	}
	switch st {
	case readiness.StepPermissions:
		rows = append(rows, w.perm.rows(w, th, l, s)...)
	case readiness.StepBYOK:
		rows = append(rows, w.byok.rows(w, th, l, s)...)
	case readiness.StepSTT:
		rows = append(rows, w.stt.rows(w, th, l, s)...)
	case readiness.StepTTS:
		rows = append(rows, w.tts.rows(w, th, l, s)...)
	case readiness.StepPlatform:
		rows = append(rows, w.plat.rows(w, th, l, s)...)
	case readiness.StepEndToEnd:
		rows = append(rows, w.e2eRows(th, l, s)...)
	}
	rows = append(rows, w.resultRows(th, l, s, st)...)
	return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(th.Theme, &w.list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			gtx.Constraints.Min.X = 0 // buttons keep their natural width
			return rows[i](gtx)
		})
	})
}

// resultRows shows the step's last outcome. A failure names the subsystem,
// the technical status and how to fix it (FR-ONB-007).
func (w *Wizard) resultRows(th *theme.Theme, l i18n.Lang, s snapshot, st readiness.Step) []layout.Widget {
	var rows []layout.Widget
	if s.running {
		return append(rows, gap(12), w.text(th.StatusLabel(theme.StatusWarn, i18n.T(l, "wiz.state.checking"))))
	}
	if s.runErr != "" {
		rows = append(rows, gap(12), w.failure(th, l, st, s.runErr))
	}
	r, ok := s.result(st)
	if !ok {
		return rows
	}
	if r.Status == readiness.Passed {
		return append(rows, gap(12), w.text(th.StatusLabel(theme.StatusOK, i18n.T(l, "wiz.result.passed"))))
	}
	return append(rows, gap(12), w.failure(th, l, st, orCode(r.Code)))
}

func orCode(c string) string {
	if c == "" {
		return "failed"
	}
	return c
}

// failure renders subsystem, technical status and fix text.
func (w *Wizard) failure(th *theme.Theme, l i18n.Lang, st readiness.Step, code string) layout.Widget {
	title := w.text(th.StatusLabel(theme.StatusError, i18n.T(l, "wiz.result.failed")))
	sub := w.text(kit.Body(th, i18n.Tf(l, "wiz.result.subsystem", i18n.T(l, "wiz.subsystem."+string(st)))))
	status := w.text(kit.Body(th, i18n.Tf(l, "wiz.result.status", code)))
	fix := w.text(kit.Body(th, i18n.Tf(l, "wiz.result.fix", fixText(l, code))))
	return func(gtx layout.Context) layout.Dimensions {
		return kit.Card(gtx, th, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(title), kit.VSpace(4), layout.Rigid(sub), layout.Rigid(status), layout.Rigid(fix))
		})
	}
}

// fixText is the fix action for a technical code.
func fixText(l i18n.Lang, code string) string {
	if k := "wiz.fix." + code; i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "wiz.fix.generic")
}

func (w *Wizard) actionBar(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	first := s.view() == readiness.Steps[0]
	return layout.Inset{Left: 24, Right: 24, Top: 8, Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if first {
					return layout.Dimensions{}
				}
				return w.button(gtx, th, ActBack, i18n.T(l, "wiz.back"), !s.running)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return w.button(gtx, th, ActDiagnostics, i18n.T(l, "wiz.diagnostics"), true)
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return w.button(gtx, th, ActQuit, i18n.T(l, "wiz.quit"), true)
					}),
				)
			}),
		)
	})
}

// checkButton is the step's Check, or Re-check once it has a result.
func (w *Wizard) checkButton(th *theme.Theme, l i18n.Lang, s snapshot, st readiness.Step, enabled bool) layout.Widget {
	act, key := ActCheck, "wiz.check"
	if _, ok := s.result(st); ok {
		act, key = ActRecheck, "wiz.recheck"
	}
	return func(gtx layout.Context) layout.Dimensions {
		return w.button(gtx, th, act, i18n.T(l, key), enabled && !s.running)
	}
}

// checkClicked reports a Check or Re-check click.
func (w *Wizard) checkClicked(gtx layout.Context) bool {
	a, b := w.clicked(gtx, ActCheck), w.clicked(gtx, ActRecheck)
	return a || b
}

func (w *Wizard) e2eRows(th *theme.Theme, l i18n.Lang, s snapshot) []layout.Widget {
	rows := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions {
			return w.button(gtx, th, ActRunFull, i18n.T(l, "wiz.e2e.run"), !s.running)
		},
		gap(12),
	}
	r, ok := s.result(readiness.StepEndToEnd)
	for _, stage := range readiness.EndToEndStages {
		ds, word := theme.StatusUnknown, i18n.T(l, "wiz.state.todo")
		if ok {
			ds, word = detailStatus(l, r.Detail[stage])
		}
		rows = append(rows, w.text(th.StatusLabel(ds, i18n.T(l, "wiz.e2e."+stage)+" — "+word)))
	}
	return rows
}

// detailStatus maps a Detail value to symbol status and word.
func detailStatus(l i18n.Lang, v string) (theme.Status, string) {
	switch v {
	case readiness.DetailOK, "passed":
		return theme.StatusOK, i18n.T(l, "wiz.state.passed")
	case "", readiness.DetailSkipped, "pending":
		return theme.StatusUnknown, i18n.T(l, "wiz.state.skipped")
	}
	return theme.StatusError, i18n.T(l, "wiz.state.failed") + " (" + v + ")"
}

func gap(dp unit.Dp) layout.Widget { return layout.Spacer{Height: dp}.Layout }
