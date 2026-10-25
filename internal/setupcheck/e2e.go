package setupcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/talk"
	"github.com/xlsft/companion/pkg/contracts"
)

// Hinter shows and clears pointing hints; overlay.Engine implements it.
type Hinter interface {
	RegisterCapture(meta contracts.CaptureMetadata)
	Apply(ctx context.Context, cmd overlay.Command) error
}

// EndToEnd is the StepEndToEnd check (FR-ONB-009): the fixed phrase goes
// through the whole Talk path. Each stage of readiness.EndToEndStages gets
// DetailOK, a failure code, or DetailSkipped once an earlier stage failed.
type EndToEnd struct {
	Listener *Listener
	Lang     func() string // UI language
	// Heard, when set, gets what the run heard ("" as a run starts), so the
	// step can show it next to a mismatch. User content: memory only.
	Heard func(text string)
	// Reply, when set, gets the model's reply ("" as a run starts), for the
	// step to show. User content: memory only.
	Reply func(text string)

	// Silent runs without the user's voice and without sound (OC-133): the
	// voice check already proved microphone, recognition and voice, so
	// "speech" and "stt" report StepSTT, "tts" reports StepTTS (DetailOK
	// when passed, else DetailSkipped), nothing is recorded and the reply
	// is not spoken. The model request is fixed text either way.
	Silent bool
	// Passed reports whether a step passed, for Silent; nil: none did.
	Passed func(readiness.Step) bool

	Capturer contracts.ScreenCapturer // nil: capture is unavailable
	Target   contracts.CaptureRequest
	// CaptureTimeout leaves room for a consent dialog (default 60 s).
	CaptureTimeout time.Duration

	Provider   talk.ProviderFunc
	LLMTimeout time.Duration // default 60 s

	Speaker    talk.Speaker
	Configured func() bool // speech output is set up
	TTSTimeout time.Duration

	// Hints is nil where only the panel fallback exists; pointing is then
	// skipped and does not block setup, like the permission's fallback.
	Hints    Hinter
	HintTime time.Duration // how long the hint stays (default 1.5 s)
}

// e2eSession tags the hint so it never mixes with a Talk session's scene.
var e2eSession = contracts.SessionRef{ID: "setup-end-to-end", Generation: 1}

func (e EndToEnd) Run(ctx context.Context) readiness.Result {
	r := &stages{detail: map[string]string{}}
	lang := e.Lang()
	if e.Heard != nil {
		e.Heard("")
	}
	if e.Reply != nil {
		e.Reply("")
	}

	if e.Silent {
		r.detail[StageSpeech] = e.voiceStage(readiness.StepSTT)
		r.detail[StageSTT] = e.voiceStage(readiness.StepSTT)
	} else if res, failed := e.listen(ctx, r, lang); failed {
		return res
	}

	shot, code := e.capture(ctx)
	if code != "" {
		return r.fail("capture", code)
	}
	// The encoded copy is all the request needs.
	img, meta, err := talk.ModelImage(shot, talk.DefaultMaxModelSide)
	shot.Release()
	if err != nil {
		return r.fail("capture", CodeCaptureFailed)
	}
	r.ok("capture")
	reply, code := e.ask(ctx, img, lang)
	if code != "" {
		return r.fail("llm", code)
	}
	r.ok("llm")
	if e.Reply != nil {
		e.Reply(reply)
	}

	if e.Silent {
		r.detail["tts"] = e.voiceStage(readiness.StepTTS)
	} else if code := say(ctx, e.Speaker, e.Configured, reply, e.TTSTimeout); code != "" {
		return r.fail("tts", code)
	} else {
		r.ok("tts")
	}

	if e.Hints == nil {
		r.detail["pointing"] = readiness.DetailSkipped
		return r.result()
	}
	if code := e.point(ctx, meta); code != "" {
		return r.fail("pointing", code)
	}
	r.ok("pointing")
	return r.result()
}

// listen records the fixed phrase; failed reports a failed stage.
func (e EndToEnd) listen(ctx context.Context, r *stages, lang string) (readiness.Result, bool) {
	heard, f := e.Listener.Listen(ctx, readiness.PhraseLanguage(lang))
	switch {
	case f != nil && f.Stage == StageSpeech:
		return r.fail(StageSpeech, f.Code), true
	case f != nil:
		r.ok(StageSpeech)
		return r.fail(StageSTT, f.Code), true
	}
	r.ok(StageSpeech)
	if e.Heard != nil {
		e.Heard(heard.Text)
	}
	if !readiness.PhraseMatches(readiness.MicPhrase(lang), heard.Text) {
		return r.fail(StageSTT, CodeMismatch), true
	}
	r.ok(StageSTT)
	return readiness.Result{}, false
}

// voiceStage is a silent run's stage taken from the voice check's step.
func (e EndToEnd) voiceStage(step readiness.Step) string {
	if e.Passed != nil && e.Passed(step) {
		return readiness.DetailOK
	}
	return readiness.DetailSkipped
}

func (e EndToEnd) capture(ctx context.Context) (*contracts.Capture, string) {
	if e.Capturer == nil {
		return nil, readiness.PermUnavailable
	}
	cctx, cancel := within(ctx, e.CaptureTimeout, time.Minute)
	defer cancel()
	caps, err := e.Capturer.Capture(cctx, e.Target)
	var shot *contracts.Capture
	for _, c := range caps {
		switch {
		case c == nil:
		case shot == nil && c.Image != nil:
			shot = c
		default:
			c.Release()
		}
	}
	if err != nil || shot == nil {
		if shot != nil {
			shot.Release()
		}
		if ctx.Err() != nil {
			return nil, CodeCanceled
		}
		return nil, permCode(err, CodeCaptureFailed)
	}
	return shot, ""
}

// e2ePrompt is the fixed request; the reply is spoken, so it asks for one
// short sentence.
const e2ePrompt = "This is the setup self-test of a desktop voice assistant. " +
	"Reply with exactly one short sentence (at most 12 words) confirming that you can see the attached screenshot. " +
	"Plain text only, no tools. Reply in the language with code %q."

// ask sends the fixed request with the screenshot and returns the reply.
func (e EndToEnd) ask(ctx context.Context, img llm.Image, lang string) (string, string) {
	if e.Provider == nil {
		return "", CodeNoProvider
	}
	ctx, cancel := within(ctx, e.LLMTimeout, time.Minute)
	defer cancel()
	p, err := e.Provider(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return "", llmCode(ctx.Err())
		}
		return "", CodeNoProvider
	}
	req := llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{
			llm.Text(fmt.Sprintf(e2ePrompt, readiness.PhraseLanguage(lang))), llm.ImagePart(img),
		}}},
		Policy:  llm.ModelPolicy{MaxOutputTokens: 200},
		Session: e2eSession,
	}
	st, err := p.Stream(ctx, req)
	if err != nil {
		return "", llmCode(err)
	}
	defer st.Close()
	var text strings.Builder
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", llmCode(err)
		}
		switch ev := ev.(type) {
		case llm.TextDelta:
			text.WriteString(ev.Text)
		case llm.Failed:
			return "", llmCode(ev.Err)
		}
	}
	reply := strings.TrimSpace(text.String())
	if reply == "" {
		return "", CodeEmptyReply
	}
	return reply, ""
}

// point shows a ring in the middle of the captured display, mapped through
// the capture's metadata like a model's point_at, then clears it.
func (e EndToEnd) point(ctx context.Context, meta contracts.CaptureMetadata) string {
	e.Hints.RegisterCapture(meta)
	show := overlay.Command{
		Op: overlay.OpShow, ID: "setup-hint", Session: e2eSession, Capture: meta.ID,
		Kind: overlay.KindRing, Points: []contracts.Point{{X: 0.5, Y: 0.5}}, Radius: 0.04,
		Style: overlay.Style{Emphasis: true}, Segment: -1,
	}
	clear := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = e.Hints.Apply(cctx, overlay.Command{Op: overlay.OpClearSession, Session: e2eSession})
	}
	if err := e.Hints.Apply(ctx, show); err != nil {
		clear()
		if errors.Is(err, contracts.ErrStale) {
			return "stale"
		}
		return CodeOverlayFailed
	}
	d := e.HintTime
	if d <= 0 {
		d = 1500 * time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
	clear()
	if ctx.Err() != nil {
		return CodeCanceled
	}
	return ""
}

// stages collects the per-stage Detail in readiness.EndToEndStages order.
type stages struct {
	detail map[string]string
}

func (s *stages) ok(stage string) { s.detail[stage] = readiness.DetailOK }

// fail records the failed stage, skips the later ones and returns the
// failed result with the stage's code.
func (s *stages) fail(stage, code string) readiness.Result {
	s.detail[stage] = code
	for _, st := range readiness.EndToEndStages {
		if _, ok := s.detail[st]; !ok {
			s.detail[st] = readiness.DetailSkipped
		}
	}
	return readiness.Result{Status: readiness.Failed, Code: code, Detail: s.detail}
}

func (s *stages) result() readiness.Result {
	return readiness.Result{Status: readiness.Passed, Detail: s.detail}
}
