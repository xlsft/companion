// Package chat turns the Talk session and the local history into the
// conversation the Home window shows (OC-71): user turns with how they were
// given, assistant replies as they stream in, and the session's state.
//
// It holds no UI code, so the transcript logic stays in the pure-Go build.
// Message text is never logged here; logs carry technical codes only.
package chat

import (
	"image"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// Role is who said a message.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
)

// Via is how a user message was given.
type Via int

const (
	ViaText Via = iota
	ViaVoice
)

// Message is one turn of the conversation.
type Message struct {
	Role Role
	Text string
	Via  Via
	// STTModel names the speech recogniser of a voice turn, e.g.
	// "whisper medium"; empty when unknown.
	STTModel string
	At       time.Time
	// Streaming is set while an assistant reply is still arriving.
	Streaming bool
	// Interrupted marks a reply cut off by Stop or a newer request.
	Interrupted bool
	// Failed marks a reply that ended with an error; Code is the technical
	// step that failed.
	Failed bool
	Code   string
	// Screens are the screenshots the request carried (user turns). An
	// entry exists whether or not the picture was kept; see Screenshot.
	Screens []Screenshot
}

// Screenshot says a request had a screen attached. Thumb is nil unless the
// user opted in to keeping screenshots: without it only the fact is shown,
// never pixels (FR-SCREEN-006).
type Screenshot struct {
	Meta  contracts.CaptureMetadata
	Thumb *image.RGBA
}

// Kept reports whether the picture itself was stored with the message.
func (s Screenshot) Kept() bool { return s.Thumb != nil }

// Phase is the session state as the chat presents it.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseListening
	PhaseTranscribing
	// PhaseThinking covers taking the screenshot and waiting for the model.
	PhaseThinking
	PhaseResponding
	PhaseSpeaking
	// PhaseAwaiting: the assistant asked a question and waits for the answer.
	PhaseAwaiting
)

// Busy reports whether a request is in flight, i.e. Stop has an effect.
func (p Phase) Busy() bool { return p != PhaseIdle && p != PhaseAwaiting }

// View is the whole chat as the UI renders it. Views are values: the
// Messages slice of a delivered view is never modified afterwards.
type View struct {
	Title    string
	Messages []Message
	Phase    Phase
	// Partial is the live transcript while listening; it must never be
	// logged.
	Partial string
	// Since is when Phase was entered.
	Since time.Time
}

// Capabilities says what the chat can do right now. A false field leaves
// its control disabled; nothing is simulated.
type Capabilities struct {
	// Text: typed requests can be sent.
	Text bool
	// Voice: the microphone and speech recognition are available.
	Voice bool
	// Hotkey is the Talk shortcut as the user presses it, e.g. "Mod+Space";
	// empty when no global shortcut is bound.
	Hotkey string
	// Model and Speech label the assistant's language model and voice for
	// the chat header; empty ones are not shown.
	Model, Speech string
}
