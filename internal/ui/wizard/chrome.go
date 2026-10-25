package wizard

import (
	"image/color"
	"slices"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// The wizard frame from the prototypes: a 272px aside with the logo, the
// step list and Diagnostics; a main column with the step counter and
// progress, the scrolling step body and a footer with the step's actions.

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

// btn lays out an action button and records it for the frame.
func (w *Wizard) btn(gtx layout.Context, th *theme.Theme, a Action, b kit.ButtonStyle, enabled bool) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, a)
	w.frame.texts = append(w.frame.texts, b.Label)
	if !enabled {
		gtx = gtx.Disabled()
	}
	return b.Layout(gtx, th, w.clickable(a))
}

// Layout draws the wizard: aside, header, current step, footer.
func (w *Wizard) Layout(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	w.frame = frameRecord{}
	s := w.snap()
	if s.view() != w.entered {
		w.enterStep(s.view())
	}
	w.update(gtx, s, l)
	s = w.snap()
	w.autoCheck(gtx, s)
	s = w.snap()

	kit.Fill(gtx, th.Pal.Bg)
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.aside(gtx, th, l, s) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.VDivider(gtx, th) }),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.header(gtx, th, l, s) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return w.content(gtx, th, l, s) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return w.footer(gtx, th, l, s) }),
			)
		}),
	)
}

// update handles the clicks of the previous frame.
func (w *Wizard) update(gtx layout.Context, s snapshot, l i18n.Lang) {
	for i, st := range readiness.Steps {
		if w.stepBtns[i].Clicked(gtx) {
			w.selectStep(s, st)
		}
	}
	i := slices.Index(readiness.Steps, s.view())
	if w.clicked(gtx, ActBack) && i > 0 {
		w.selectStep(s, readiness.Steps[i-1])
	}
	// Next only moves to a step the Gate has unlocked: it is the way
	// forward after going Back, never past an unpassed step.
	if w.clicked(gtx, ActNext) && i+1 < len(readiness.Steps) {
		w.selectStep(s, readiness.Steps[i+1])
	}
	if w.clicked(gtx, ActDiagnostics) && w.cfg.OnDiagnostics != nil {
		w.cfg.OnDiagnostics()
	}
	if w.clicked(gtx, ActQuit) && w.cfg.OnQuit != nil {
		go w.cfg.OnQuit()
	}
	if i := w.langDD.Update(gtx, len(i18n.Languages)); i >= 0 && w.cfg.SetLanguage != nil {
		go w.cfg.SetLanguage(i18n.Languages[i].Code)
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

func (w *Wizard) aside(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	width := min(gtx.Dp(th.Dp(272)), gtx.Constraints.Max.X*2/5)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = width, width
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
	kit.Fill(gtx, th.Pal.Sidebar)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Left: th.Dp(20), Right: th.Dp(20)}.Layout(gtx, kit.FixedH(th, 54, func(gtx layout.Context) layout.Dimensions {
				if w.cfg.Version != "" {
					w.frame.texts = append(w.frame.texts, "v"+w.cfg.Version)
				}
				return kit.LogoVersion(gtx, th, 15, w.cfg.Version)
			}))
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		// The steps scroll when large text leaves too little height, so
		// the footer with Diagnostics and the language always fits.
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			w.asideList.Axis = layout.Vertical
			return material.List(th.Theme, &w.asideList).Layout(gtx, len(readiness.Steps), func(gtx layout.Context, i int) layout.Dimensions {
				in := layout.Inset{Top: th.Dp(2), Left: th.Dp(8), Right: th.Dp(8)}
				if i == 0 {
					in.Top = th.Dp(12)
				}
				return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return w.stepEntry(gtx, th, l, s, i, readiness.Steps[i])
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Divider(gtx, th) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(th.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return w.asideFoot(gtx, th, l)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if w.cfg.AsideExtra == nil {
				return layout.Dimensions{}
			}
			// With its own 8dp margins it lines up with the 12dp footer above.
			return layout.Inset{Left: th.Dp(4), Right: th.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return w.cfg.AsideExtra(gtx, th, l)
			})
		}),
	)
}

// asideFoot is Diagnostics plus the language picker.
func (w *Wizard) asideFoot(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	diag := func(gtx layout.Context) layout.Dimensions {
		w.frame.actions = append(w.frame.actions, ActDiagnostics)
		txt := i18n.T(l, "wiz.diagnostics")
		w.frame.texts = append(w.frame.texts, txt)
		return kit.NavItem(gtx, th, w.clickable(ActDiagnostics), icon.Heartbeat, txt, false)
	}
	var meta layout.Widget
	if w.cfg.SetLanguage != nil {
		meta = func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return w.langPicker(gtx, th, l)
			})
		}
	}
	return kit.VStack(th, 2, diag, meta)(gtx)
}

// langPicker is the interface language select, each language under its
// own name.
func (w *Wizard) langPicker(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	w.frame.actions = append(w.frame.actions, ActLanguage)
	names := make([]string, len(i18n.Languages))
	sel := 0
	for i, lg := range i18n.Languages {
		names[i] = lg.Native
		if lg.Code == l {
			sel = i
		}
	}
	w.frame.texts = append(w.frame.texts, names[sel])
	if w.langDD.Opened() {
		w.frame.texts = append(w.frame.texts, names...)
	}
	// Full aside width, like the Diagnostics item above it.
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return w.langDD.Layout(gtx, th, names, sel, names[sel])
}

// stepState is how the aside shows a step.
type stepState int

const (
	stepLocked stepState = iota
	stepOpen             // unlocked, not passed
	stepFailed
	stepDone
	stepCurrent
)

func stepStateOf(s snapshot, st readiness.Step) stepState {
	if st == s.view() {
		return stepCurrent
	}
	if !s.unlocked(st) {
		return stepLocked
	}
	r, ok := s.result(st)
	switch {
	case ok && r.Status == readiness.Passed:
		return stepDone
	case ok && r.Status == readiness.Failed:
		return stepFailed
	}
	return stepOpen
}

// stepWord is the state of a step in words, for the accessible name: the
// aside shows it as a shape (check, lock, number).
func stepWord(s snapshot, st readiness.Step) string {
	if !s.unlocked(st) {
		return "wiz.state.locked"
	}
	if s.running && st == s.view() {
		return "wiz.state.checking"
	}
	r, ok := s.result(st)
	switch {
	case ok && r.Status == readiness.Passed:
		return "wiz.state.passed"
	case ok && r.Status == readiness.Failed:
		return "wiz.state.failed"
	}
	return "wiz.state.todo"
}

func (w *Wizard) stepEntry(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot, i int, st readiness.Step) layout.Dimensions {
	state := stepStateOf(s, st)
	unlocked := s.unlocked(st)
	if unlocked {
		w.frame.actions = append(w.frame.actions, ActSelectStep)
	}
	name := i18n.T(l, "wiz.step."+string(st))
	word := i18n.T(l, stepWord(s, st))
	w.frame.texts = append(w.frame.texts, name, word)
	if !unlocked {
		gtx = gtx.Disabled()
	}
	num := string(rune('1' + i))
	var mark, trail layout.Widget
	fg, bg := th.Pal.Fg, color.NRGBA{}
	plain := kit.NumberDisc(th, 22, num, th.Pal.Fg2, color.NRGBA{}, th.Pal.Border, false)
	switch state {
	case stepCurrent:
		bg = th.Pal.AccentSoft
		mark = kit.NumberDisc(th, 22, num, th.Pal.AccentFg, th.Pal.AccentLine, color.NRGBA{}, true)
	case stepDone:
		fg = th.Pal.Fg2
		mark = kit.Disc(th, 22, th.Pal.OKSoft, color.NRGBA{}, kit.IconW(th, icon.Check, 12, th.Pal.OK))
	case stepFailed:
		mark = plain
		trail = kit.IconW(th, icon.Close, 14, th.Pal.Error)
	case stepOpen:
		mark = plain
	default:
		fg = th.Pal.Subtle
		mark = kit.NumberDisc(th, 22, num, th.Pal.Subtle, color.NRGBA{}, th.Pal.Border, false)
		trail = kit.IconW(th, icon.Lock, 14, th.Pal.Subtle)
	}
	b := &w.stepBtns[i]
	if state != stepCurrent && unlocked && b.Hovered() {
		bg = th.Pal.Hover
	}
	return kit.FocusRing(gtx, th, gtx.Focused(b), func(gtx layout.Context) layout.Dimensions {
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.DescriptionOp(name + ", " + word).Add(gtx.Ops)
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return kit.Box(gtx, th, bg, color.NRGBA{}, 4, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: th.Dp(12), Right: th.Dp(12)}.Layout(gtx, kit.FixedH(th, 40, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(mark),
						kit.HSpace(th.Dp(10)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							t := kit.Text(th, 14, name, fg)
							t.MaxLines = 1
							return t.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if trail == nil {
								return layout.Dimensions{}
							}
							return trail(gtx)
						}),
					)
				}))
			})
		})
	})
}

func (w *Wizard) header(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	i := slices.Index(readiness.Steps, s.view())
	n := len(readiness.Steps)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Left: th.Dp(32), Right: th.Dp(11)}.Layout(gtx, kit.FixedH(th, 54, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			// Half a step for the one on screen, as in the prototypes.
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return kit.Progress(gtx, th, (float32(i)+0.5)/float32(n))
			}),
			kit.HSpace(th.Dp(16)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				desc := i18n.T(l, "wiz.quit")
				w.frame.actions = append(w.frame.actions, ActQuit)
				w.frame.texts = append(w.frame.texts, desc)
				return kit.IconButton(gtx, th, w.clickable(ActQuit), icon.Close, desc)
			}),
		)
	}))
}

// stepBody is what a step contributes to the frame: rows of the page,
// each spanning the content column.
type stepBody struct {
	rows []layout.Widget
}

func (w *Wizard) content(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	st := s.view()
	var body stepBody
	switch st {
	case readiness.StepPermissions:
		body = w.perm.body(w, th, l, s)
	case readiness.StepBYOK:
		body = w.byok.body(w, th, l, s)
	case readiness.StepSTT:
		body = w.stt.body(w, th, l, s)
	case readiness.StepTTS:
		body = w.tts.body(w, th, l, s)
	case readiness.StepPlatform:
		body = w.plat.body(w, th, l, s)
	case readiness.StepEndToEnd:
		body = w.e2eBody(th, l, s)
	}
	head := kit.VStack(th, 8,
		w.text(kit.Heading(th, i18n.T(l, "wiz.head."+string(st)))),
		kit.MaxWidth(th, 640, w.text(kit.Lead(th, i18n.T(l, "wiz.step."+string(st)+".intro")))),
	)
	rows := append([]layout.Widget{head}, body.rows...)
	rows = append(rows, w.resultRows(th, l, s, st)...)
	// The list spans the whole column so its scrollbar sits at the window
	// edge; the page padding applies per row and every row spans the rest.
	return material.List(th.Theme, &w.list).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		side := kit.PageMargin(gtx, th, 56)
		in := layout.Inset{Top: th.Dp(22), Left: side, Right: side}
		if i == 0 {
			in.Top = th.Dp(36)
		}
		if i == len(rows)-1 {
			// Keeps the last row clear of the footer divider.
			in.Bottom = th.Dp(28)
		}
		return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return rows[i](gtx)
		})
	})
}

// resultRows shows a failed check: the subsystem, the technical status and
// how to fix it (FR-ONB-007). Success and progress are in the footer. The
// last failure stays up while the check runs again: a failing check is
// repeated every few seconds, and hiding it for each run made it blink.
func (w *Wizard) resultRows(th *theme.Theme, l i18n.Lang, s snapshot, st readiness.Step) []layout.Widget {
	var rows []layout.Widget
	if s.runErr != "" {
		rows = append(rows, w.failure(th, l, st, s.runErr))
	}
	if r, ok := s.result(st); ok && r.Status != readiness.Passed {
		rows = append(rows, w.failure(th, l, st, orCode(r.Code)))
	}
	return rows
}

func orCode(c string) string {
	if c == "" {
		return "failed"
	}
	return c
}

// shownCode is a technical code as the wizard displays it. The binding
// code names the system's window manager, which the wizard never does;
// Diagnostics keeps the original.
func shownCode(c string) string {
	if c == readiness.PermNeedsBinding {
		return "needs_key_binding"
	}
	return c
}

// failure is a tinted status row in its own panel: what failed, the
// subsystem and status code, and how to fix it.
func (w *Wizard) failure(th *theme.Theme, l i18n.Lang, st readiness.Step, code string) layout.Widget {
	return rows(th, w.statusRow(th, kit.StatusRow{
		Icon:  icon.Close,
		Title: i18n.T(l, "wiz.result.failed"),
		Caption: i18n.Tf(l, "wiz.result.subsystem", i18n.T(l, "wiz.subsystem."+string(st))) + " · " +
			i18n.Tf(l, "wiz.result.status", shownCode(code)),
		Detail: i18n.Tf(l, "wiz.result.fix", fixText(l, code)),
		Status: theme.StatusError,
		Badge:  i18n.T(l, "wiz.e2e.failed"),
		Tint:   true,
	}))
}

// fixText is the fix action for a technical code.
func fixText(l i18n.Lang, code string) string {
	if k := "wiz.fix." + code; i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "wiz.fix.generic")
}

// footerSpec is a step's footer: a status line and up to two actions.
type footerSpec struct {
	status    string
	secondary layout.Widget
	primary   layout.Widget
}

func (w *Wizard) footer(gtx layout.Context, th *theme.Theme, l i18n.Lang, s snapshot) layout.Dimensions {
	var f footerSpec
	switch s.view() {
	case readiness.StepPermissions:
		f = w.perm.footer(w, th, l, s)
	case readiness.StepBYOK:
		f = w.byok.footer(w, th, l, s)
	case readiness.StepSTT:
		f = w.stt.footer(w, th, l, s)
	case readiness.StepTTS:
		f = w.tts.footer(w, th, l, s)
	case readiness.StepPlatform:
		f = w.plat.footer(w, th, l, s)
	case readiness.StepEndToEnd:
		f = w.e2eFooter(th, l, s)
	}
	if f.status == "" {
		f.status = w.statusLine(l, s)
	}
	first := s.view() == readiness.Steps[0]
	var back, status layout.Widget
	if !first {
		back = func(gtx layout.Context) layout.Dimensions {
			return w.btn(gtx, th, ActBack, kit.ButtonStyle{Kind: kit.Ghost, Label: i18n.T(l, "wiz.back"), Icon: icon.Left}, !s.running)
		}
	}
	if f.status != "" {
		st := kit.Text(th, 12, f.status, th.Pal.Muted)
		st.MaxLines = 1 // the footer keeps its height at large text sizes
		status = w.text(st)
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Left: th.Dp(32), Right: th.Dp(32)}.Layout(gtx, kit.FixedH(th, 72, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Spread(kit.HStack(th, 8, back, status), kit.HStack(th, 8, f.secondary, f.primary))(gtx)
	}))
}

// statusLine is the default footer text: the step's last outcome.
func (w *Wizard) statusLine(l i18n.Lang, s snapshot) string {
	if s.running {
		return i18n.T(l, "wiz.state.checking")
	}
	r, ok := s.result(s.view())
	if !ok {
		return ""
	}
	key := "wiz.result.failed"
	if r.Status == readiness.Passed {
		key = "wiz.result.passed"
	}
	txt := i18n.T(l, key)
	if !r.At.IsZero() {
		txt += " · " + i18n.Tf(l, "wiz.result.at", r.At.Local().Format("15:04"))
	}
	return txt
}

// next is the primary Next button: enabled once the Gate has unlocked the
// following step, i.e. this one passed.
func (w *Wizard) next(th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	i := slices.Index(readiness.Steps, s.view())
	ok := i+1 < len(readiness.Steps) && s.unlocked(readiness.Steps[i+1]) && !s.running
	return func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, ActNext, kit.ButtonStyle{Kind: kit.Primary, Label: i18n.T(l, "wiz.next")}, ok)
	}
}

// checkButton is the step's Check, or Re-check once it has a result.
func (w *Wizard) checkButton(th *theme.Theme, l i18n.Lang, s snapshot, st readiness.Step, kind kit.ButtonKind, ic icon.Name, enabled bool) layout.Widget {
	act, key := ActCheck, "wiz.check"
	if _, ok := s.result(st); ok {
		act, key = ActRecheck, "wiz.recheck"
	}
	return func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, act, kit.ButtonStyle{Kind: kind, Label: i18n.T(l, key), Icon: ic}, enabled && !s.running)
	}
}

// checkClicked reports a Check or Re-check click.
func (w *Wizard) checkClicked(gtx layout.Context) bool {
	a, b := w.clicked(gtx, ActCheck), w.clicked(gtx, ActRecheck)
	return a || b
}
