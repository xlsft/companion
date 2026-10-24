// Package talk implements the Talk pipeline behind the session state
// machine (OC-58, OC-60): it records, captures, asks the model with the
// screenshot, streams the answer to the UI and to speech, and executes the
// model's overlay tool calls.
package talk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/speech"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

// Overlay is the part of overlay.Engine the pipeline drives.
type Overlay interface {
	RegisterCapture(meta contracts.CaptureMetadata)
	Apply(ctx context.Context, cmd overlay.Command) error
}

// Speaker plays one piece of text and returns when playback ends. started
// is called once when audio becomes audible.
type Speaker interface {
	Say(ctx context.Context, text string, started func()) error
}

// Recorder records the microphone into the local STT.
type Recorder interface {
	Start(ctx context.Context, ref contracts.SessionRef, emit session.Emitter) error
	Finish(ctx context.Context, ref contracts.SessionRef) (string, error)
}

// Memory supplies conversation context and remembered facts, and learns
// from finished turns (*memory.Service).
type Memory interface {
	Prepare(ctx context.Context, text string) memory.Prepared
	Record(ctx context.Context, ref contracts.SessionRef, user, answer string)
}

// ProviderFunc returns the provider for the Talk role (OC-25). It is
// called per request so profile changes apply to the next request.
type ProviderFunc func(ctx context.Context) (llm.Provider, error)

// DefaultMaxModelSide bounds a screenshot sent to the model unless the
// Orchestrator sets MaxModelSide.
const DefaultMaxModelSide = defaultMaxModelSide

const (
	defaultMaxModelSide  = 1568
	defaultMaxToolRounds = 4
	maxHistoryTurns      = 8
)

// Orchestrator implements session.Steps.
type Orchestrator struct {
	Provider ProviderFunc
	Overlay  Overlay // nil: pointing tools are not offered
	Speaker  Speaker // nil: answers are text only
	Capturer contracts.ScreenCapturer
	Recorder Recorder
	Target   contracts.CaptureRequest
	Policy   llm.ModelPolicy
	// SystemPrompt overrides the built-in prompt.
	SystemPrompt string
	// MaxModelSide bounds the longest side of a screenshot sent to the model.
	MaxModelSide  int
	MaxToolRounds int
	// Attachments is told which screenshots a request carries. nil: the
	// chat is not told.
	Attachments contracts.AttachmentSink
	// KeepScreenshots reports whether the sent screenshot may be handed on
	// for storing; nil or false keeps pixels out of the sink.
	KeepScreenshots func() bool
	// Memory, when set, replaces the per-session history below: earlier
	// turns come from the open conversation (also after it was reopened)
	// and the system prompt gets what is known about the user.
	Memory Memory
	// Guide runs walkthroughs (OC-68); nil: the model cannot start one.
	Guide Guide

	mu sync.Mutex
	// history keeps the text of previous turns of the current SessionID for
	// follow-ups; screenshots of past turns are not resent.
	historyID contracts.SessionID
	history   []llm.Message
	// metas are the latest prepared screenshots' metadata, no pixels, so a
	// walkthrough step drawn on one can tell where its hints are.
	metas []contracts.CaptureMetadata
}

// maxMetas bounds metas; a walkthrough only looks up its plan's screenshot
// right after it was made.
const maxMetas = 8

var _ session.Steps = (*Orchestrator)(nil)

func (o *Orchestrator) StartRecording(ctx context.Context, ref contracts.SessionRef, emit session.Emitter) error {
	if o.Guide != nil {
		// The user talks over the step: let them be heard.
		o.Guide.Hush()
	}
	if o.Recorder == nil {
		return contracts.Unsupported(contracts.CapMicrophone)
	}
	return o.Recorder.Start(ctx, ref, emit)
}

func (o *Orchestrator) FinishRecording(ctx context.Context, ref contracts.SessionRef) (string, error) {
	if o.Recorder == nil {
		return "", contracts.Unsupported(contracts.CapMicrophone)
	}
	return o.Recorder.Finish(ctx, ref)
}

func (o *Orchestrator) Capture(ctx context.Context, _ contracts.SessionRef) ([]*contracts.Capture, error) {
	if o.Capturer == nil {
		return nil, nil
	}
	return o.Capturer.Capture(ctx, o.Target)
}

// Speak is used by the session manager only when Reason did not stream
// speech itself.
func (o *Orchestrator) Speak(ctx context.Context, _ contracts.SessionRef, text string, emit session.Emitter) error {
	if o.Speaker == nil {
		return nil
	}
	return o.Speaker.Say(ctx, text, func() { emit(session.SpeechStarted{}) })
}

func (o *Orchestrator) Reason(ctx context.Context, ref contracts.SessionRef, req session.Request, emit session.Emitter) (session.Reply, error) {
	var guide walkthrough.View
	if o.Guide != nil {
		// Whatever the user says during a walkthrough goes to the model,
		// which knows the steps and sees the screen: "done, what's next?",
		// "I did that already", "go back" or a question. Its answer takes
		// the floor from the step being spoken.
		guide = o.Guide.View()
		o.Guide.Hush()
	}
	provider, err := o.Provider(ctx)
	if err != nil {
		releaseAll(req.Captures)
		return session.Reply{}, fmt.Errorf("talk provider: %w", err)
	}

	images, primary, err := o.prepareCaptures(ref, req.Captures)
	releaseAll(req.Captures)
	if err != nil {
		return session.Reply{}, err
	}

	user := llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(req.Text)}}
	for _, img := range images {
		user.Content = append(user.Content, llm.ImagePart(img))
	}
	sys := o.systemPrompt(len(images) > 0, guide)
	var prior []llm.Message
	if o.Memory != nil {
		p := o.Memory.Prepare(ctx, req.Text)
		prior = p.History
		if p.System != "" {
			sys += "\n\n" + p.System
		}
	} else {
		prior = o.priorTurns(ref.ID, req.FollowUp)
	}
	msgs := append([]llm.Message{{Role: llm.RoleSystem, Content: []llm.Part{llm.Text(sys)}}}, prior...)
	msgs = append(msgs, user)

	var tools []llm.Tool
	if o.Overlay != nil && primary != "" {
		for _, t := range overlay.Tools() {
			tools = append(tools, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
		}
	}
	if o.Guide != nil {
		specs := []overlay.ToolSpec{walkthrough.StartTool()}
		if guide.Active() {
			specs = append(specs, walkthrough.ControlTool(), walkthrough.ReviseTool())
		}
		for _, t := range specs {
			tools = append(tools, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
		}
	}

	sp := o.startSpeech(ctx, emit)
	text, next, err := o.converse(ctx, provider, ref, primary, guide, msgs, tools, emit, sp)
	speechErr := sp.finish()
	if err != nil {
		return session.Reply{}, err
	}
	if speechErr != nil && ctx.Err() == nil {
		emit(session.SpeechFailed{Err: speechErr})
	}
	// Started or moved only now, after the answer was spoken, so the step
	// is not talked over.
	switch {
	case ctx.Err() != nil:
	case next.plan != nil:
		if err := o.Guide.Start(ctx, *next.plan, primary); err != nil {
			return session.Reply{}, fmt.Errorf("walkthrough: %w", err)
		}
	case next.control != nil:
		// Moved meanwhile (auto-advance, the card): the decision is stale.
		err := o.Guide.Control(ctx, *next.control, guide.Index)
		if err != nil && !errors.Is(err, walkthrough.ErrMoved) && !errors.Is(err, walkthrough.ErrNotActive) {
			return session.Reply{}, fmt.Errorf("walkthrough: %w", err)
		}
	}
	if o.Memory != nil {
		o.Memory.Record(ctx, ref, req.Text, text)
	} else {
		o.remember(ref.ID, req.Text, text)
	}
	return session.Reply{Text: text}, nil
}

// guideNext is what a conversation asks of the walkthrough once its
// answer is spoken.
type guideNext struct {
	plan    *walkthrough.Plan
	control *walkthrough.Control
}

// converse runs model rounds until the model stops calling tools, or has
// planned or moved a walkthrough: then the steps speak for themselves and
// what to do with the walkthrough is returned.
func (o *Orchestrator) converse(ctx context.Context, p llm.Provider, ref contracts.SessionRef, capture contracts.CaptureID,
	guide walkthrough.View, msgs []llm.Message, tools []llm.Tool, emit session.Emitter, sp *speechPipe) (string, guideNext, error) {
	rounds := o.MaxToolRounds
	if rounds <= 0 {
		rounds = defaultMaxToolRounds
	}
	var answer string
	for round := 0; ; round++ {
		res, err := o.stream(ctx, p, llm.ChatRequest{Messages: msgs, Tools: tools, Policy: o.Policy, Session: ref}, emit, sp)
		if err != nil {
			return "", guideNext{}, err
		}
		answer += res.text
		if len(res.calls) == 0 {
			sp.flush()
			return answer, guideNext{}, nil
		}
		if round+1 >= rounds {
			// Out of tool budget: keep what was said rather than failing.
			sp.flush()
			return answer, guideNext{}, nil
		}
		assistant := llm.Message{Role: llm.RoleAssistant, ToolCalls: res.calls}
		if res.thinking.Text != "" || res.thinking.Signature != "" {
			assistant.Content = append(assistant.Content, res.thinking)
		}
		if res.text != "" {
			assistant.Content = append(assistant.Content, llm.Text(res.text))
		}
		msgs = append(msgs, assistant)
		var next guideNext
		for _, call := range res.calls {
			// A cancelled generation must not start another action.
			if err := ctx.Err(); err != nil {
				return "", guideNext{}, err
			}
			switch {
			case o.Guide == nil:
			case call.Name == walkthrough.ToolStart:
				var msg llm.Message
				msg, next.plan = startTool(capture, call)
				msgs = append(msgs, msg)
				continue
			case call.Name == walkthrough.ToolControl && guide.Active():
				c, err := walkthrough.FromControlCall(call.Arguments, guide.Total)
				if err != nil {
					msgs = append(msgs, toolResult(call, err.Error(), true))
					continue
				}
				next.control = &c
				msgs = append(msgs, toolResult(call, "done", false))
				continue
			case call.Name == walkthrough.ToolRevise && guide.Active():
				msgs = append(msgs, o.reviseTool(ctx, call))
				continue
			}
			msgs = append(msgs, o.runTool(ctx, ref, capture, call))
		}
		if next.plan != nil {
			// A new walkthrough replaces the one in progress.
			next.control = nil
		}
		if next.plan != nil || next.control != nil {
			sp.flush()
			return answer, next, nil
		}
	}
}

// startTool validates a start_walkthrough call. An invalid plan goes back
// to the model so it can correct it.
func startTool(capture contracts.CaptureID, call llm.ToolCall) (llm.Message, *walkthrough.Plan) {
	plan, err := walkthrough.FromToolCall(call.Arguments, capture != "")
	if err != nil {
		return toolResult(call, err.Error(), true), nil
	}
	return toolResult(call, "started", false), &plan
}

// reviseTool applies a revise_step call to the walkthrough in progress.
func (o *Orchestrator) reviseTool(ctx context.Context, call llm.ToolCall) llm.Message {
	r, err := walkthrough.FromReviseCall(call.Arguments)
	if err == nil {
		err = o.Guide.Revise(ctx, r)
	}
	if err != nil {
		return toolResult(call, err.Error(), true)
	}
	return toolResult(call, "revised", false)
}

func toolResult(call llm.ToolCall, text string, isErr bool) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, IsError: isErr, Content: []llm.Part{llm.Text(text)}}
}

type roundResult struct {
	text     string
	thinking llm.Part
	calls    []llm.ToolCall
}

func (o *Orchestrator) stream(ctx context.Context, p llm.Provider, req llm.ChatRequest, emit session.Emitter, sp *speechPipe) (roundResult, error) {
	var res roundResult
	st, err := p.Stream(ctx, req)
	if err != nil {
		return res, err
	}
	defer st.Close()
	res.thinking = llm.Thinking("", "")
	for {
		ev, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return res, err
		}
		switch ev := ev.(type) {
		case llm.TextDelta:
			res.text += ev.Text
			emit(session.LLMDelta{Text: ev.Text})
			sp.push(ev.Text)
		case llm.ThinkingDelta:
			res.thinking.Text += ev.Text
			if ev.Signature != "" {
				res.thinking.Signature = ev.Signature
			}
		case llm.ToolCallCompleted:
			// Text before a tool call is usually the sentence the drawing
			// belongs to; speak it now instead of waiting for more text.
			sp.flush()
			res.calls = append(res.calls, llm.ToolCall{ID: ev.ID, Name: ev.Name, Arguments: ev.Arguments})
		case llm.Failed:
			return res, ev.Err
		case llm.Completed:
			// Keep reading until EOF so the stream closes cleanly.
		}
	}
}

// runTool executes an overlay tool call and returns the tool result for
// the model. Problems are reported back to the model as an error result so
// it can correct itself; they never fail the session.
func (o *Orchestrator) runTool(ctx context.Context, ref contracts.SessionRef, capture contracts.CaptureID, call llm.ToolCall) llm.Message {
	msg, _ := o.runToolCmds(ctx, ref, capture, call)
	return msg
}

// runToolCmds is runTool that also returns the commands put on screen.
func (o *Orchestrator) runToolCmds(ctx context.Context, ref contracts.SessionRef, capture contracts.CaptureID, call llm.ToolCall) (llm.Message, []overlay.Command) {
	result := func(text string, isErr bool) llm.Message {
		return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, IsError: isErr, Content: []llm.Part{llm.Text(text)}}
	}
	if o.Overlay == nil {
		return result("drawing is not available", true), nil
	}
	cmds, err := overlay.FromToolCall(overlay.CallContext{
		Session: ref, Capture: capture, CallID: call.ID, Segment: -1,
	}, call.Name, call.Arguments)
	if err != nil {
		return result(err.Error(), true), nil
	}
	for i, c := range cmds {
		if err := o.Overlay.Apply(ctx, c); err != nil {
			if errors.Is(err, contracts.ErrStale) {
				return result("the screen changed since the screenshot; describe the location in words instead", true), cmds[:i]
			}
			return result("could not draw: "+err.Error(), true), cmds[:i]
		}
	}
	return result("shown", false), cmds
}

// rememberMeta keeps meta for captureMeta.
func (o *Orchestrator) rememberMeta(meta contracts.CaptureMetadata) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.metas = append(o.metas, meta)
	if n := len(o.metas); n > maxMetas {
		o.metas = append([]contracts.CaptureMetadata(nil), o.metas[n-maxMetas:]...)
	}
}

// captureMeta is the metadata of a recently prepared screenshot.
func (o *Orchestrator) captureMeta(id contracts.CaptureID) (contracts.CaptureMetadata, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(o.metas) - 1; i >= 0; i-- {
		if o.metas[i].ID == id {
			return o.metas[i], true
		}
	}
	return contracts.CaptureMetadata{}, false
}

func (o *Orchestrator) systemPrompt(withScreen bool, guide walkthrough.View) string {
	if o.SystemPrompt != "" {
		return o.SystemPrompt
	}
	p := "You are a desktop assistant that answers out loud. Keep answers short and spoken-style: " +
		"plain sentences, no Markdown tables or code blocks unless asked. Answer in the user's language."
	if withScreen {
		p += " A screenshot of the user's screen is attached. When the user asks where something is or " +
			"what to click, call point_at with the element's position on the screenshot and say in one " +
			"sentence what it is. Use annotate to explain with arrows or outlines. Never invent elements " +
			"that are not visible; say so if you can't find it."
	}
	if o.Guide != nil {
		p += " When the task takes several actions on screen, guide the user with start_walkthrough instead of " +
			"listing every step at once."
		if guide.Active() {
			p += " " + guide.Brief() + " The attached screenshot shows where the user is now. Call " +
				"walkthrough_control when the user says they did the step or asks what's next (next), already did " +
				"it or later ones (next or goto), wants to go back, hear it again, pause, resume or stop, or names a " +
				"step to jump to; then say at most a few words, the step is shown and spoken for you. Judge by what " +
				"they say and the screenshot. If they only ask a question, answer it briefly without moving; if the " +
				"screen shows the current step must be done differently, call revise_step."
		}
	}
	return p
}

func (o *Orchestrator) priorTurns(id contracts.SessionID, followUp bool) []llm.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !followUp || id != o.historyID {
		o.historyID, o.history = id, nil
		return nil
	}
	return append([]llm.Message(nil), o.history...)
}

func (o *Orchestrator) remember(id contracts.SessionID, question, answer string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if id != o.historyID {
		return
	}
	o.history = append(o.history,
		llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(question)}},
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Part{llm.Text(answer)}},
	)
	if n := len(o.history); n > 2*maxHistoryTurns {
		o.history = append([]llm.Message(nil), o.history[n-2*maxHistoryTurns:]...)
	}
}

func releaseAll(caps []*contracts.Capture) {
	for _, c := range caps {
		if c != nil {
			c.Release()
		}
	}
}

// speechPipe feeds answer text to the Speaker sentence by sentence while
// the model is still streaming (FR-VOICE-004).
type speechPipe struct {
	seg     *speech.Segmenter
	queue   chan string
	done    chan struct{}
	err     error
	enabled bool
}

func (o *Orchestrator) startSpeech(ctx context.Context, emit session.Emitter) *speechPipe {
	sp := &speechPipe{seg: speech.NewSegmenter(), queue: make(chan string, 64), done: make(chan struct{}), enabled: o.Speaker != nil}
	if !sp.enabled {
		close(sp.done)
		return sp
	}
	pf, _ := o.Speaker.(Prefetcher)
	go func() {
		defer close(sp.done)
		started := false
		say := func(text string) error {
			return o.Speaker.Say(ctx, text, func() {
				if !started {
					started = true
					emit(session.SpeechStarted{})
				}
			})
		}
		var next string
		haveNext, open := false, true
		for open || haveNext {
			var text string
			if haveNext {
				text, haveNext = next, false
			} else if text, open = <-sp.queue; !open {
				break
			}
			if sp.err != nil || ctx.Err() != nil {
				continue // drain
			}
			var err error
			if pf == nil || !open {
				err = say(text)
			} else {
				// While this segment plays, take the next one off the queue
				// and start synthesising it, so it is ready on hand-off.
				res := make(chan error, 1)
				go func() { res <- say(text) }()
				select {
				case err = <-res:
				case next, open = <-sp.queue:
					if open {
						haveNext = true
						pf.Prefetch(ctx, next)
					}
					err = <-res
				}
			}
			if err != nil && ctx.Err() == nil {
				sp.err = err
			}
		}
		if started && sp.err == nil {
			emit(session.SpeechDone{})
		}
	}()
	return sp
}

func (sp *speechPipe) push(delta string) {
	if !sp.enabled {
		return
	}
	for _, s := range sp.seg.Push(delta) {
		sp.queue <- s
	}
}

func (sp *speechPipe) flush() {
	if !sp.enabled {
		return
	}
	for _, s := range sp.seg.Flush() {
		sp.queue <- s
	}
}

// finish waits until everything queued was spoken and returns the first
// playback error.
func (sp *speechPipe) finish() error {
	if sp.enabled {
		close(sp.queue)
	}
	<-sp.done
	return sp.err
}
