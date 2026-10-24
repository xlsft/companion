package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Request is what Steps.Reason receives.
type Request struct {
	Mode Mode
	// Text is the final transcript or the typed request. Never log it.
	Text string
	// Captures are owned by Reason from here on; it must Release them.
	Captures []*contracts.Capture
	// FollowUp is true when this generation continues the SessionID of a
	// previous one (barge-in or an answer to StateAwaitingInput).
	FollowUp bool
}

// Reply is the final model output of a generation.
type Reply struct {
	Text string
	// NeedInput moves the session to StateAwaitingInput instead of Idle.
	NeedInput bool
}

// Emitter posts an event stamped with the generation it was created for.
// Steps use it for streamed results (partials, deltas, speech start); it
// stays valid after the step returns and is a no-op once the generation or
// Manager is gone.
type Emitter func(Event)

// Steps are the operations the Manager starts on transitions. The
// orchestrator (OC-58) implements them over STT, capture, LLM and TTS.
//
// Each call runs in its own goroutine with the generation's context, which
// is cancelled with a cause (ErrDone, ErrCancelled, ErrInterrupted, ...)
// when the generation ends; implementations must return promptly then.
// A returned error ends the generation in StateFailed unless the context
// was already cancelled. Results are turned into events by the Manager, so
// an implementation cannot forget to stamp them.
type Steps interface {
	// StartRecording opens the mic and returns once recording runs. In
	// hands-free mode it emits UtteranceEnd when the VAD detects the end
	// of speech; it may emit TranscriptPartial at any time.
	StartRecording(ctx context.Context, ref contracts.SessionRef, emit Emitter) error
	// FinishRecording stops the mic and returns the final transcript.
	FinishRecording(ctx context.Context, ref contracts.SessionRef) (string, error)
	// Capture takes the screenshot(s) for the request.
	Capture(ctx context.Context, ref contracts.SessionRef) ([]*contracts.Capture, error)
	// Reason runs the model, emitting LLMDelta while streaming. If it
	// streams speech itself it emits SpeechStarted and SpeechDone and the
	// Manager will not call Speak.
	Reason(ctx context.Context, ref contracts.SessionRef, req Request, emit Emitter) (Reply, error)
	// Speak plays text and returns when playback finished. It should emit
	// SpeechStarted when audio becomes audible.
	Speak(ctx context.Context, ref contracts.SessionRef, text string, emit Emitter) error
}

// ReleaseReporter is the part of contracts.HotkeyService the Manager needs.
type ReleaseReporter interface{ SupportsRelease() bool }

type Config struct {
	Settings
	// Hotkeys, if set, is consulted at every session start: without key-up
	// events hold-to-talk degrades to toggle (PRD §4.2).
	Hotkeys ReleaseReporter
	Now     func() time.Time
	NewID   func() contracts.SessionID
}

// Stats are counters for diagnostics and tests.
type Stats struct {
	// Dropped counts events of a generation that is no longer active.
	Dropped uint64
	// Ignored counts events that are not legal in the current state.
	Ignored uint64
}

// generation is loop-owned state of one request.
type generation struct {
	ref      contracts.SessionRef
	mode     Mode
	settings Settings
	followUp bool
	ctx      context.Context
	cancel   context.CancelCauseFunc
	emit     Emitter

	recording   bool // mic is open (until the stop gesture)
	transcribed bool // text is final
	captured    bool
	text        string
	captures    []*contracts.Capture

	responded       bool // some reply text was published
	llmDone         bool
	reply           Reply
	speechStarted   bool
	speechRequested bool
	speechDone      bool
}

// Manager owns at most one active Talk session. All state is touched only
// by the loop goroutine; public methods talk to it through inbox.
type Manager struct {
	steps Steps
	cfg   Config

	inbox     chan any // Event or func()
	closing   chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once
	stepsWG   sync.WaitGroup
	subsWG    sync.WaitGroup

	current atomic.Pointer[Snapshot]
	dropped atomic.Uint64
	ignored atomic.Uint64

	// loop-owned
	settings Settings
	state    State
	since    time.Time
	g        *generation
	gen      uint64
	subs     map[*subscriber]struct{}
}

// New starts a Manager. Close must be called to stop it.
func New(steps Steps, cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	m := &Manager{
		steps:    steps,
		cfg:      cfg,
		inbox:    make(chan any, 64),
		closing:  make(chan struct{}),
		stopped:  make(chan struct{}),
		settings: normalize(cfg.Settings),
		state:    StateIdle,
		since:    cfg.Now(),
		subs:     map[*subscriber]struct{}{},
	}
	m.current.Store(&Snapshot{State: StateIdle, Since: m.since})
	go m.loop()
	return m
}

func randomID() contracts.SessionID {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return contracts.SessionID(hex.EncodeToString(b[:]))
}

func normalize(s Settings) Settings {
	switch s.VoiceMode {
	case ModeToggle, ModeHandsFree:
	default:
		s.VoiceMode = ModeHoldToTalk
	}
	return s
}

// Post queues an event. It blocks only while the inbox is full and returns
// ErrClosed after Close.
func (m *Manager) Post(ev Event) error {
	if ev == nil {
		return nil
	}
	select {
	case <-m.closing:
		return ErrClosed
	default:
	}
	select {
	case m.inbox <- ev:
		return nil
	case <-m.closing:
		return ErrClosed
	}
}

// SetSettings changes preferences for the next generation.
func (m *Manager) SetSettings(s Settings) error {
	s = normalize(s)
	if !m.exec(func() { m.settings = s }) {
		return ErrClosed
	}
	return nil
}

// Current returns the last published snapshot.
func (m *Manager) Current() Snapshot { return *m.current.Load() }

func (m *Manager) Stats() Stats {
	return Stats{Dropped: m.dropped.Load(), Ignored: m.ignored.Load()}
}

// Close cancels the active generation with ErrClosed, stops the loop and
// waits for all step goroutines and subscribers to finish.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { close(m.closing) })
	<-m.stopped
	m.stepsWG.Wait()
	m.subsWG.Wait()
	return nil
}

// exec runs fn on the loop after every previously posted event.
func (m *Manager) exec(fn func()) bool {
	done := make(chan struct{})
	select {
	case m.inbox <- func() { fn(); close(done) }:
	case <-m.closing:
		return false
	}
	select {
	case <-done:
		return true
	case <-m.stopped:
		return false
	}
}

func (m *Manager) loop() {
	defer close(m.stopped)
	for {
		select {
		case <-m.closing:
			m.shutdown()
			return
		case it := <-m.inbox:
			switch it := it.(type) {
			case func():
				it()
			case Event:
				m.handle(it)
			}
		}
	}
}

func (m *Manager) shutdown() {
	if m.g != nil {
		m.retire(ErrClosed)
	}
	for s := range m.subs {
		s.stop()
	}
	clear(m.subs)
}

func (m *Manager) handle(ev Event) {
	ref := ev.ref()
	_, isInput := ev.(userInput)
	stale := m.g == nil || ref != m.g.ref
	if isInput && ref == (contracts.SessionRef{}) {
		stale = false
	}
	if stale {
		m.dropped.Add(1)
		release(ev)
		return
	}
	var ok bool
	if isInput {
		ok = m.handleInput(ev)
	} else {
		ok = m.handleResult(ev)
	}
	if !ok {
		m.ignored.Add(1)
		release(ev)
	}
}

func (m *Manager) handleInput(ev Event) bool {
	g := m.g
	switch ev := ev.(type) {
	case Cancel:
		if g == nil {
			return false
		}
		m.end(StateCancelled, "cancel", ErrCancelled, nil)
		return true
	case HotkeyDown:
		if g != nil && g.recording {
			if g.mode == ModeHoldToTalk {
				return false // key repeat
			}
			m.stopListening()
			return true
		}
		mode := m.hotkeyMode()
		return m.newRequest(func(id contracts.SessionID, follow bool) { m.startVoice(id, mode, follow) })
	case HotkeyUp:
		if g == nil || !g.recording || g.mode != ModeHoldToTalk {
			return false
		}
		m.stopListening()
		return true
	case ToggleTalk:
		if g != nil && g.recording {
			m.stopListening()
			return true
		}
		mode := ModeToggle
		if m.settings.VoiceMode == ModeHandsFree {
			mode = ModeHandsFree
		}
		return m.newRequest(func(id contracts.SessionID, follow bool) { m.startVoice(id, mode, follow) })
	case SubmitText:
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			return false
		}
		return m.newRequest(func(id contracts.SessionID, follow bool) { m.startText(id, text, follow) })
	}
	return false
}

// newRequest starts a generation from Idle, by barge-in, or as the answer
// to StateAwaitingInput. Other states ignore new requests.
func (m *Manager) newRequest(start func(id contracts.SessionID, followUp bool)) bool {
	switch {
	case m.g == nil:
		start(m.cfg.NewID(), false)
	case m.state.bargeable():
		id := m.g.ref.ID
		m.end(StateInterrupted, "barge_in", ErrInterrupted, nil)
		start(id, true)
	case m.state == StateAwaitingInput:
		id := m.g.ref.ID
		m.retire(ErrDone)
		start(id, true)
	default:
		return false
	}
	return true
}

func (m *Manager) hotkeyMode() Mode {
	mode := m.settings.VoiceMode
	if mode == ModeHoldToTalk && m.cfg.Hotkeys != nil && !m.cfg.Hotkeys.SupportsRelease() {
		return ModeToggle
	}
	return mode
}

func (m *Manager) handleResult(ev Event) bool {
	g := m.g
	switch ev := ev.(type) {
	case Failure:
		err := ev.Err
		if err == nil {
			err = ErrFailed
		}
		m.end(StateFailed, ev.Op, fmt.Errorf("%w: %s: %w", ErrFailed, ev.Op, err), err)
		return true
	case TranscriptPartial:
		if g.transcribed || (m.state != StateListening && m.state != StateTranscribing) {
			return false
		}
		m.publishText(ev.Text)
		return true
	case UtteranceEnd:
		if !g.recording || g.mode != ModeHandsFree {
			return false
		}
		m.stopListening()
		return true
	case TranscriptFinal:
		if m.state != StateTranscribing {
			return false
		}
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			m.complete("empty_transcript")
			return true
		}
		g.text, g.transcribed = text, true
		m.afterInput()
		return true
	case CaptureDone:
		if m.state != StateCapturing || g.captured {
			return false
		}
		g.captures, g.captured = ev.Captures, true
		switch {
		case g.transcribed:
			m.reason()
		case g.recording:
			m.setState(StateListening, "")
		default: // stop gesture arrived while capturing at start
			m.startTranscribing()
		}
		return true
	case LLMDelta:
		switch m.state {
		case StateReasoning:
			g.responded = true
			m.setState(StateResponding, ev.Text)
		case StateResponding, StateSpeaking:
			g.responded = true
			m.publishText(ev.Text)
		default:
			return false
		}
		return true
	case SpeechStarted:
		if g.speechStarted || (m.state != StateReasoning && m.state != StateResponding) {
			return false
		}
		g.speechStarted = true
		m.setState(StateSpeaking, "")
		return true
	case SpeechDone:
		if g.speechDone || !(g.speechStarted || g.speechRequested) {
			return false
		}
		g.speechDone = true
		m.maybeFinish()
		return true
	case LLMDone:
		if g.llmDone || !m.state.bargeable() {
			return false
		}
		g.llmDone = true
		g.reply = Reply{Text: ev.Text, NeedInput: ev.NeedInput}
		if !g.responded && ev.Text != "" {
			g.responded = true
			if m.state == StateReasoning {
				m.setState(StateResponding, ev.Text)
			} else {
				m.publishText(ev.Text)
			}
		}
		m.maybeFinish()
		return true
	}
	return false
}

func (m *Manager) newGeneration(id contracts.SessionID, mode Mode, followUp bool) *generation {
	m.gen++
	ctx, cancel := context.WithCancelCause(context.Background())
	g := &generation{
		ref:      contracts.SessionRef{ID: id, Generation: m.gen},
		mode:     mode,
		settings: m.settings,
		followUp: followUp,
		ctx:      ctx,
		cancel:   cancel,
	}
	ref := g.ref
	g.emit = func(ev Event) {
		if ev != nil {
			if m.Post(ev.withRef(ref)) != nil {
				release(ev)
			}
		}
	}
	m.g = g
	return g
}

func (m *Manager) startVoice(id contracts.SessionID, mode Mode, followUp bool) {
	g := m.newGeneration(id, mode, followUp)
	g.recording = true
	if g.settings.Capture == CaptureAtStart {
		m.setState(StateCapturing, "")
		m.runCapture()
	} else {
		m.setState(StateListening, "")
	}
	m.run("start_recording", func(ctx context.Context) (Event, error) {
		return nil, m.steps.StartRecording(ctx, g.ref, g.emit)
	})
}

func (m *Manager) startText(id contracts.SessionID, text string, followUp bool) {
	g := m.newGeneration(id, ModeText, followUp)
	g.text, g.transcribed = text, true
	m.afterInput()
}

func (m *Manager) stopListening() {
	m.g.recording = false
	if m.state == StateCapturing {
		return // CaptureDone continues with transcription
	}
	m.startTranscribing()
}

func (m *Manager) startTranscribing() {
	ref := m.g.ref
	m.setState(StateTranscribing, "")
	m.run("finish_recording", func(ctx context.Context) (Event, error) {
		text, err := m.steps.FinishRecording(ctx, ref)
		return TranscriptFinal{Ref: ref, Text: text}, err
	})
}

func (m *Manager) afterInput() {
	g := m.g
	if g.settings.Capture != CaptureNever && !g.captured {
		m.setState(StateCapturing, "")
		m.runCapture()
		return
	}
	m.reason()
}

func (m *Manager) runCapture() {
	ref := m.g.ref
	m.run("capture", func(ctx context.Context) (Event, error) {
		caps, err := m.steps.Capture(ctx, ref)
		return CaptureDone{Ref: ref, Captures: caps}, err
	})
}

func (m *Manager) reason() {
	g := m.g
	req := Request{Mode: g.mode, Text: g.text, Captures: g.captures, FollowUp: g.followUp}
	g.captures = nil
	m.setState(StateReasoning, "")
	m.run("reason", func(ctx context.Context) (Event, error) {
		r, err := m.steps.Reason(ctx, g.ref, req, g.emit)
		return LLMDone{Ref: g.ref, Text: r.Text, NeedInput: r.NeedInput}, err
	})
}

// maybeFinish ends the response once both the model and the speech are done.
func (m *Manager) maybeFinish() {
	g := m.g
	if !g.llmDone || (g.speechStarted && !g.speechDone) {
		return
	}
	if !g.speechStarted && !g.speechRequested && g.settings.SpeakReplies && g.reply.Text != "" {
		g.speechRequested = true
		text := g.reply.Text
		m.run("speak", func(ctx context.Context) (Event, error) {
			return SpeechDone{Ref: g.ref}, m.steps.Speak(ctx, g.ref, text, g.emit)
		})
		return
	}
	if g.speechRequested && !g.speechDone {
		return
	}
	if g.reply.NeedInput {
		m.setState(StateAwaitingInput, "")
		return
	}
	m.complete("done")
}

// run starts a step for the active generation and posts its result unless
// the generation ended meanwhile.
func (m *Manager) run(op string, fn func(context.Context) (Event, error)) {
	g := m.g
	m.stepsWG.Add(1)
	go func() {
		defer m.stepsWG.Done()
		ev, err := fn(g.ctx)
		if g.ctx.Err() != nil {
			release(ev)
			return
		}
		if err != nil {
			release(ev)
			ev = Failure{Ref: g.ref, Op: op, Err: err}
		}
		if ev != nil && m.Post(ev) != nil {
			release(ev)
		}
	}()
}

// retire cancels the active generation without publishing anything.
func (m *Manager) retire(cause error) {
	g := m.g
	g.cancel(cause)
	release(CaptureDone{Captures: g.captures})
	g.captures = nil
	m.g = nil
}

func (m *Manager) complete(reason string) {
	g := m.g
	m.retire(ErrDone)
	m.publish(Snapshot{Ref: g.ref, State: StateIdle, Mode: g.mode, Reason: reason})
}

// end publishes a terminal state for the active generation. Except for
// StateInterrupted, whose successor follows immediately, Idle comes next.
func (m *Manager) end(state State, reason string, cause, err error) {
	g := m.g
	m.retire(cause)
	m.publish(Snapshot{Ref: g.ref, State: state, Mode: g.mode, Reason: reason, Err: err})
	if state != StateInterrupted {
		m.publish(Snapshot{Ref: g.ref, State: StateIdle, Mode: g.mode, Reason: reason})
	}
}

func (m *Manager) setState(s State, text string) {
	m.publish(Snapshot{Ref: m.g.ref, State: s, Mode: m.g.mode, Text: text})
}

func (m *Manager) publishText(text string) {
	m.publish(Snapshot{Ref: m.g.ref, State: m.state, Mode: m.g.mode, Text: text, Since: m.since})
}

func (m *Manager) publish(s Snapshot) {
	if s.Since.IsZero() {
		m.since = m.cfg.Now()
		s.Since = m.since
	}
	m.state = s.State
	m.current.Store(&s)
	for sub := range m.subs {
		sub.push(s)
	}
}
