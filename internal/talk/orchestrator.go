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

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/overlay"
	"github.com/xlsft/openclick/internal/session"
	"github.com/xlsft/openclick/internal/speech"
	"github.com/xlsft/openclick/pkg/contracts"
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

// ProviderFunc returns the provider for the Talk role (OC-25). It is
// called per request so profile changes apply to the next request.
type ProviderFunc func(ctx context.Context) (llm.Provider, error)

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

	mu sync.Mutex
	// history keeps the text of previous turns of the current SessionID for
	// follow-ups; screenshots of past turns are not resent.
	historyID contracts.SessionID
	history   []llm.Message
}

var _ session.Steps = (*Orchestrator)(nil)

func (o *Orchestrator) StartRecording(ctx context.Context, ref contracts.SessionRef, emit session.Emitter) error {
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
	provider, err := o.Provider(ctx)
	if err != nil {
		releaseAll(req.Captures)
		return session.Reply{}, fmt.Errorf("talk provider: %w", err)
	}

	images, primary, err := o.prepareCaptures(req.Captures)
	releaseAll(req.Captures)
	if err != nil {
		return session.Reply{}, err
	}

	user := llm.Message{Role: llm.RoleUser, Content: []llm.Part{llm.Text(req.Text)}}
	for _, img := range images {
		user.Content = append(user.Content, llm.ImagePart(img))
	}
	msgs := append([]llm.Message{{Role: llm.RoleSystem, Content: []llm.Part{llm.Text(o.systemPrompt(len(images) > 0))}}},
		o.priorTurns(ref.ID, req.FollowUp)...)
	msgs = append(msgs, user)

	var tools []llm.Tool
	if o.Overlay != nil && primary != "" {
		for _, t := range overlay.Tools() {
			tools = append(tools, llm.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
		}
	}

	sp := o.startSpeech(ctx, emit)
	text, err := o.converse(ctx, provider, ref, primary, msgs, tools, emit, sp)
	speechErr := sp.finish()
	if err != nil {
		return session.Reply{}, err
	}
	if speechErr != nil && ctx.Err() == nil {
		emit(session.SpeechFailed{Err: speechErr})
	}
	o.remember(ref.ID, req.Text, text)
	return session.Reply{Text: text}, nil
}

// converse runs model rounds until the model stops calling tools.
func (o *Orchestrator) converse(ctx context.Context, p llm.Provider, ref contracts.SessionRef, capture contracts.CaptureID,
	msgs []llm.Message, tools []llm.Tool, emit session.Emitter, sp *speechPipe) (string, error) {
	rounds := o.MaxToolRounds
	if rounds <= 0 {
		rounds = defaultMaxToolRounds
	}
	var answer string
	for round := 0; ; round++ {
		res, err := o.stream(ctx, p, llm.ChatRequest{Messages: msgs, Tools: tools, Policy: o.Policy, Session: ref}, emit, sp)
		if err != nil {
			return "", err
		}
		answer += res.text
		if len(res.calls) == 0 {
			sp.flush()
			return answer, nil
		}
		if round+1 >= rounds {
			// Out of tool budget: keep what was said rather than failing.
			sp.flush()
			return answer, nil
		}
		assistant := llm.Message{Role: llm.RoleAssistant, ToolCalls: res.calls}
		if res.thinking.Text != "" || res.thinking.Signature != "" {
			assistant.Content = append(assistant.Content, res.thinking)
		}
		if res.text != "" {
			assistant.Content = append(assistant.Content, llm.Text(res.text))
		}
		msgs = append(msgs, assistant)
		for _, call := range res.calls {
			msgs = append(msgs, o.runTool(ctx, ref, capture, call))
		}
	}
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
	result := func(text string, isErr bool) llm.Message {
		return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, IsError: isErr, Content: []llm.Part{llm.Text(text)}}
	}
	if o.Overlay == nil {
		return result("drawing is not available", true)
	}
	cmds, err := overlay.FromToolCall(overlay.CallContext{
		Session: ref, Capture: capture, CallID: call.ID, Segment: -1,
	}, call.Name, call.Arguments)
	if err != nil {
		return result(err.Error(), true)
	}
	for _, c := range cmds {
		if err := o.Overlay.Apply(ctx, c); err != nil {
			if errors.Is(err, contracts.ErrStale) {
				return result("the screen changed since the screenshot; describe the location in words instead", true)
			}
			return result("could not draw: "+err.Error(), true)
		}
	}
	return result("shown", false)
}

func (o *Orchestrator) systemPrompt(withScreen bool) string {
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
