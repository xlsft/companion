package wizard

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/xlsft/companion/internal/models"
	"github.com/xlsft/companion/internal/models/hw"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/kit"
	"github.com/xlsft/companion/internal/ui/theme"
)

// screen is a page of the setup: the agent (model provider), the voice
// and microphone, and the finish. The Gate's six steps run under them:
// the agent screen is StepBYOK, the voice screen StepTTS and StepSTT (one
// check, the voice heard by the microphone), the finish StepEndToEnd;
// permissions and platform are checked in the background and only surface
// on the finish when they need the user.
type screen int

const (
	screenAgent screen = iota
	screenVoice
	screenDone
)

// screenSteps are the Gate steps a screen must pass before Next.
var screenSteps = map[screen][]readiness.Step{
	screenAgent: {readiness.StepBYOK},
	screenVoice: {readiness.StepTTS, readiness.StepSTT},
	screenDone:  {readiness.StepEndToEnd},
}

// passed reports whether every step of sc passed.
func (s snapshot) passed(sc screen) bool {
	for _, st := range screenSteps[sc] {
		if r, ok := s.result(st); !ok || r.Status != readiness.Passed {
			return false
		}
	}
	return true
}

// screenFor is where setup starts: the first screen with work left.
func screenFor(s snapshot) screen {
	switch {
	case !s.passed(screenAgent):
		return screenAgent
	case !s.passed(screenVoice):
		return screenVoice
	}
	return screenDone
}

// enterScreen runs when the screen changes: the previous screen's work is
// cancelled and the new one loads its data.
func (w *Wizard) enterScreen(sc screen) {
	w.scr, w.entered = sc, readiness.Step("")
	w.mu.Lock()
	w.stepCancel()
	w.stepCtx, w.stepCancel = context.WithCancel(w.ctx)
	ctx := w.stepCtx
	w.runErr = ""
	w.mu.Unlock()
	switch sc {
	case screenAgent:
		w.byok.load(w, ctx)
	case screenVoice:
		w.tts.load(w, ctx)
		w.stt.load(w, ctx)
	}
}

// background keeps the checks that need nothing from the user current
// while setup is open: permissions (repeated while they fail, so a fix in
// the system settings shows up by itself) and the platform report.
type background struct {
	mu      sync.Mutex
	running map[readiness.Step]bool
	last    map[readiness.Step]time.Time
}

// bgEvery is how often a failing background check is repeated.
const bgEvery = 3 * time.Second

func (w *Wizard) backgroundChecks(gtx layout.Context, s snapshot) {
	for _, st := range []readiness.Step{readiness.StepPermissions, readiness.StepPlatform} {
		r, ok := s.result(st)
		if ok && r.Status == readiness.Passed {
			continue
		}
		b := &w.bg
		b.mu.Lock()
		if b.running == nil {
			b.running, b.last = map[readiness.Step]bool{}, map[readiness.Step]time.Time{}
		}
		due := b.last[st].Add(bgEvery)
		if b.running[st] || (ok && gtx.Now.Before(due)) {
			b.mu.Unlock()
			if ok {
				gtx.Execute(op.InvalidateCmd{At: due})
			}
			continue
		}
		b.running[st], b.last[st] = true, gtx.Now
		b.mu.Unlock()
		go func() {
			if _, err := w.cfg.Gate.Run(w.ctx, st); err != nil && !errors.Is(err, context.Canceled) {
				w.cfg.Log.Warn("setup background check did not run", "step", string(st), "code", runErrCode(err))
			}
			b.mu.Lock()
			b.running[st] = false
			b.mu.Unlock()
			w.cfg.Invalidate()
		}()
	}
}

// modelPhase is where the speech model's automatic download stands.
type modelPhase int

const (
	modelIdle modelPhase = iota
	modelDownloading
	modelPreparing
	modelWaiting // no network: tried again by itself
	modelFailed
	modelReady
)

// speechModel downloads the recommended speech model as soon as setup
// opens, without asking: the user can change it later in Settings. It runs
// on the wizard's own context, so leaving a screen does not stop it.
type speechModel struct {
	mu      sync.Mutex
	phase   modelPhase
	prog    models.Progress
	readyAt time.Time
	retry   chan struct{}
}

// modelRetryEvery is how often a download that found no network retries.
const modelRetryEvery = 5 * time.Second

func (m *speechModel) state() (modelPhase, models.Progress, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.phase, m.prog, m.readyAt
}

func (m *speechModel) set(fn func()) {
	m.mu.Lock()
	fn()
	m.mu.Unlock()
}

func (w *Wizard) startSpeechModel() {
	b := w.cfg.Backends.STT
	m := &w.model
	if b == nil {
		return
	}
	m.set(func() { m.retry = make(chan struct{}, 1) })
	go func() {
		for {
			err := w.installSpeechModel(b)
			if err == nil || w.ctx.Err() != nil {
				return
			}
			phase := modelFailed
			var ne net.Error
			if errors.As(err, &ne) {
				phase = modelWaiting // no network: it comes back by itself
			}
			m.set(func() { m.phase = phase })
			w.cfg.Invalidate()
			w.cfg.Log.Warn("speech model download failed", "code", installErrCode(err))
			var again <-chan time.Time
			if phase == modelWaiting {
				again = time.After(modelRetryEvery)
			}
			select {
			case <-w.ctx.Done():
				return
			case <-m.retry:
			case <-again:
			}
		}
	}()
}

// installSpeechModel makes sure a speech model is active: one already
// installed is used as it is, otherwise the recommended one is downloaded.
func (w *Wizard) installSpeechModel(b STT) error {
	m := &w.model
	if inst, err := b.Installed(); err == nil && len(inst) > 0 {
		m.set(func() { m.phase, m.readyAt = modelReady, time.Now() })
		w.cfg.Invalidate()
		return nil
	}
	cat, err := b.Catalog()
	if err != nil {
		return err
	}
	rec, _ := hw.Recommend(b.Hardware(w.ctx), cat, w.cfg.SpeechLanguage())
	man, ok := cat.Find(rec.ID)
	if !ok {
		return errors.New("recommended model not in the catalog")
	}
	m.set(func() { m.phase, m.prog = modelDownloading, models.Progress{Total: man.Size} })
	w.cfg.Invalidate()
	var last time.Time
	err = b.Install(w.ctx, man, func(p models.Progress) {
		m.set(func() {
			m.prog = p
			if p.Total > 0 && p.Done >= p.Total {
				m.phase = modelPreparing // verifying and unpacking
			}
		})
		if now := time.Now(); now.Sub(last) > 100*time.Millisecond {
			last = now
			w.cfg.Invalidate()
		}
	})
	if err != nil {
		return err
	}
	m.set(func() { m.phase, m.readyAt = modelReady, time.Now() })
	w.cfg.Invalidate()
	return nil
}

// retrySpeechModel starts a failed download again.
func (w *Wizard) retrySpeechModel() {
	m := &w.model
	m.mu.Lock()
	ch := m.retry
	m.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// modelReadyFor is how long the "ready" chip stays before it goes away.
const modelReadyFor = 5 * time.Second

// modelChip is the speech model's state in the footer; nothing once it
// has been ready for a few seconds.
func (w *Wizard) modelChip(gtx layout.Context, th *theme.Theme, l i18n.Lang) layout.Dimensions {
	phase, prog, readyAt := w.model.state()
	var p kit.Pill
	switch phase {
	case modelIdle:
		return layout.Dimensions{}
	case modelDownloading:
		p = kit.Pill{Lead: kit.LeadRing, Text: i18n.T(l, "wiz.model.downloading")}
		if prog.Total > 0 {
			p.Progress = float32(prog.Done) / float32(prog.Total)
			p.Meta = percent(prog.Done, prog.Total)
		}
	case modelPreparing:
		p = kit.Pill{Lead: kit.LeadRing, Progress: 1, Text: i18n.T(l, "wiz.model.preparing")}
	case modelWaiting:
		p = kit.Pill{Lead: kit.LeadRing, Text: i18n.T(l, "wiz.model.waiting")}
		if prog.Total > 0 {
			p.Progress = float32(prog.Done) / float32(prog.Total)
			p.Meta = percent(prog.Done, prog.Total)
		}
	case modelFailed:
		p = kit.Pill{Lead: kit.LeadWarning, Text: i18n.T(l, "wiz.model.failed"), Action: i18n.T(l, "wiz.model.retry")}
	case modelReady:
		gone := readyAt.Add(modelReadyFor)
		if !gtx.Now.Before(gone) {
			return layout.Dimensions{}
		}
		gtx.Execute(op.InvalidateCmd{At: gone})
		p = kit.Pill{Lead: kit.LeadCheck, Text: i18n.T(l, "wiz.model.ready")}
	}
	if phase == modelFailed && w.clicked(gtx, ActRetryModel) {
		w.retrySpeechModel()
	}
	w.frame.texts = append(w.frame.texts, p.Text)
	if p.Action != "" {
		w.frame.actions = append(w.frame.actions, ActRetryModel)
	}
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(th.Dp(280)))
	return p.Layout(gtx, th, w.clickable(ActRetryModel), nil)
}

func percent(done, total int64) string {
	return strconv.Itoa(int(done*100/total)) + "%"
}
