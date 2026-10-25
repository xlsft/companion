package wizard

import (
	"gioui.org/layout"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/icon"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// stageState is one end-to-end stage as its card shows it.
type stageState int

const (
	stageWaiting stageState = iota // no run yet
	stageRunning
	stageDone
	stageFailed
	stageNotRun // a run ended before this stage
)

func stageOf(s snapshot, stage string) (stageState, string) {
	if s.running {
		return stageRunning, ""
	}
	r, ok := s.result(readiness.StepEndToEnd)
	if !ok {
		return stageWaiting, ""
	}
	switch v := r.Detail[stage]; v {
	case readiness.DetailOK, "passed":
		return stageDone, ""
	case "", readiness.DetailSkipped, "pending":
		return stageNotRun, ""
	default:
		return stageFailed, v
	}
}

// stageIcons are the tiles of the end-to-end stages.
var stageIcons = map[string]icon.Name{
	"speech": icon.Mic, "stt": icon.Voice, "capture": icon.Computer,
	"llm": icon.Sparkles, "tts": icon.Volume, "pointing": icon.Aiming2,
}

func (w *Wizard) e2eBody(th *theme.Theme, l i18n.Lang, s snapshot) stepBody {
	rs := make([]layout.Widget, len(readiness.EndToEndStages))
	for i, stage := range readiness.EndToEndStages {
		rs[i] = w.stageRow(th, l, s, stage)
	}
	return stepBody{rows: []layout.Widget{w.e2ePrompt(th, l, s), rows(th, rs...)}}
}

// e2ePrompt says what the test wants from the user before it listens: the
// phrase to read once the check starts, and afterwards what was heard, so
// a mismatch is something the user can act on.
func (w *Wizard) e2ePrompt(th *theme.Theme, l i18n.Lang, s snapshot) layout.Widget {
	key := "wiz.e2e.sayAfterStart"
	if s.running {
		key = "wiz.e2e.sayNow"
	}
	capt := kit.Caption(th, i18n.T(l, key))
	if s.running {
		capt.Color = th.Pal.AccentText
	}
	head := w.text(capt)
	phrase := w.text(kit.Text(th, 16, "«"+readiness.MicPhrase(string(l))+"»", th.Pal.Fg))
	var heard layout.Widget
	if t, ok := w.cfg.Backends.STT.(EndToEndTranscript); ok && !s.running {
		if st, code := stageOf(s, "stt"); st == stageFailed && code != "" {
			if txt := t.LastEndToEndTranscript(); txt != "" {
				heard = w.text(kit.Text(th, 13, i18n.Tf(l, "wiz.stt.heard", txt), th.Pal.Fg2))
			}
		}
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		border := th.Pal.Border
		if s.running {
			border = th.Pal.AccentLine
		}
		return kit.Box(gtx, th, th.Pal.Bg, border, 4, kit.Pad(th, 12, 14, kit.VStack(th, 4, head, phrase, heard)))
	}
}

// stageRow is one stage as a status row, like a permission: a failed
// stage is tinted and shows its code in place of the description.
func (w *Wizard) stageRow(th *theme.Theme, l i18n.Lang, s snapshot, stage string) layout.Widget {
	st, code := stageOf(s, stage)
	r := kit.StatusRow{
		Icon:    stageIcons[stage],
		Title:   i18n.T(l, "wiz.e2e."+stage),
		Caption: i18n.T(l, "wiz.e2e."+stage+".d"),
	}
	var word string
	switch st {
	case stageDone:
		word, r.Status = "wiz.e2e.done", theme.StatusOK
	case stageFailed:
		word, r.Status, r.Tint = "wiz.e2e.failed", theme.StatusError, true
		r.Caption = i18n.Tf(l, "wiz.result.status", code)
	case stageRunning:
		word, r.Status = "wiz.e2e.running", theme.StatusUnknown
	case stageNotRun:
		word, r.Status, r.IconColor = "wiz.e2e.notRun", theme.StatusUnknown, th.Pal.Subtle
	default:
		word, r.Status = "wiz.e2e.waiting", theme.StatusUnknown
	}
	r.Badge = i18n.T(l, word)
	line := r.Title + " — " + r.Badge
	if code != "" {
		line += " (" + code + ")"
	}
	w.frame.texts = append(w.frame.texts, line)
	return w.statusRow(th, r)
}

func (w *Wizard) e2eFooter(th *theme.Theme, l i18n.Lang, s snapshot) footerSpec {
	b := kit.ButtonStyle{Kind: kit.Primary, Icon: icon.PlayFill, Label: i18n.T(l, "wiz.e2e.run")}
	r, ok := s.result(readiness.StepEndToEnd)
	switch {
	case s.running:
		// Kept in the accent look while running; runCheck ignores clicks
		// until the run ends.
		b.Icon, b.Label = icon.Loading3, i18n.T(l, "wiz.e2e.inProgress")
	case ok:
		b.Icon, b.Label = icon.Refresh2, i18n.T(l, "wiz.e2e.runAgain")
	}
	f := footerSpec{primary: func(gtx layout.Context) layout.Dimensions {
		return w.btn(gtx, th, ActRunFull, b, true)
	}}
	if ok && !s.running {
		n := 0
		for _, stage := range readiness.EndToEndStages {
			if st, _ := stageOf(s, stage); st == stageDone {
				n++
			}
		}
		f.status = i18n.Tf(l, "wiz.e2e.stagesPassed", n, len(readiness.EndToEndStages))
		if !r.At.IsZero() {
			f.status += " · " + i18n.Tf(l, "wiz.result.at", r.At.Local().Format("15:04"))
		}
	}
	return f
}
