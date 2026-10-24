// Package session implements the voice/text Talk session state machine
// (PRD §8.4, FR-VOICE-001…009). A Manager owns at most one active session
// and serialises every input through a single event loop, so states never
// need locks. Each request runs as a generation with its own context; when
// the generation ends (done, cancelled, interrupted, failed) the context is
// cancelled and every late event stamped with its SessionRef is dropped.
package session

import (
	"errors"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// State is a user-visible session state (FR-VOICE-009).
type State string

const (
	StateIdle         State = "idle"
	StateListening    State = "listening"    // mic open, recording
	StateTranscribing State = "transcribing" // mic closed, final decode
	StateCapturing    State = "capturing"    // taking the screenshot
	StateReasoning    State = "reasoning"    // request sent, no output yet
	StateResponding   State = "responding"   // model output streaming
	StateSpeaking     State = "speaking"     // TTS audio playing
	// StateAwaitingInput: the model asked a question; the next request
	// continues the same SessionID.
	StateAwaitingInput State = "awaiting_input"

	// Terminal states are published once for the ending generation. All but
	// StateInterrupted are followed by StateIdle; an interrupted generation
	// is immediately followed by the generation that replaced it.
	StateCancelled   State = "cancelled"
	StateFailed      State = "failed"
	StateInterrupted State = "interrupted"
)

// Mode is how the current request was started (FR-VOICE-001).
type Mode string

const (
	ModeHoldToTalk Mode = "hold"       // record while the hotkey is held
	ModeToggle     Mode = "toggle"     // press to start, press again to stop
	ModeHandsFree  Mode = "hands_free" // press to start, VAD ends the utterance
	ModeText       Mode = "text"       // typed request, no recording
)

// CaptureOrder decides when the screenshot is taken (FR-VOICE-002).
type CaptureOrder int

const (
	// CaptureAtEnd captures after the transcript is final:
	// Listening → Transcribing → Capturing → Reasoning.
	CaptureAtEnd CaptureOrder = iota
	// CaptureAtStart captures the screen as it was when the user started
	// talking: Capturing → Listening → Transcribing → Reasoning. Recording
	// starts together with the capture so no audio is lost; Listening is
	// published only after the capture so the listening indicator cannot
	// end up in the screenshot.
	CaptureAtStart
	// CaptureNever sends the request without a screenshot.
	CaptureNever
)

// Settings are user preferences; a change applies from the next generation.
type Settings struct {
	// VoiceMode is the hotkey behaviour: ModeHoldToTalk (default),
	// ModeToggle or ModeHandsFree.
	VoiceMode Mode
	Capture   CaptureOrder
	// SpeakReplies makes the Manager call Steps.Speak with the final reply
	// unless speech was already streamed during the response.
	SpeakReplies bool
}

// Snapshot is what observers (overlay, Home window) render.
type Snapshot struct {
	Ref   contracts.SessionRef
	State State
	Mode  Mode
	// Reason is a technical code for terminal and idle states
	// ("cancel", "barge_in", "done", "empty_transcript", or the failed step).
	Reason string
	// Err is set for StateFailed, and on the final Idle snapshot with
	// Reason "done_without_speech" when the reply could not be spoken.
	Err error
	// Text is content carried by this update: a partial transcript while
	// Listening/Transcribing, a model delta while Responding/Speaking, or
	// the full reply if the model did not stream. Empty for pure state
	// changes. It must never be logged.
	Text string
	// Since is when State was entered; text-only updates keep it.
	Since time.Time
}

// Causes passed to context.CancelCauseFunc when a generation ends, so
// downstream operations can tell why their context was cancelled.
var (
	ErrDone        = errors.New("session finished")
	ErrCancelled   = errors.New("session cancelled")
	ErrInterrupted = errors.New("session interrupted by a new request")
	ErrFailed      = errors.New("session failed")
	ErrClosed      = errors.New("session manager closed")
)

// bargeable states are those where the user's input is complete, so a new
// request supersedes the pending answer (FR-VOICE-005).
func (s State) bargeable() bool {
	return s == StateReasoning || s == StateResponding || s == StateSpeaking
}
