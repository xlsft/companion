package session

import "github.com/xlsft/openclick/pkg/contracts"

// Event is an input to the Manager. Every event carries the SessionRef it
// belongs to; the Manager drops events whose Ref is not the active
// generation (FR-VOICE-008) and counts them in Stats.Dropped.
//
// User inputs (HotkeyDown, HotkeyUp, ToggleTalk, SubmitText, Cancel) may
// leave Ref zero to mean "whatever is active"; a non-zero Ref targets that
// generation only, e.g. a cancel button on a card of an older request.
// Step results must carry the Ref they were started with; Emitter does that.
type Event interface {
	ref() contracts.SessionRef
	withRef(contracts.SessionRef) Event
}

type userInput interface {
	Event
	userInput()
}

// HotkeyDown is a press of the Talk hotkey.
type HotkeyDown struct{ Ref contracts.SessionRef }

// HotkeyUp is a release of the Talk hotkey; only hold-to-talk uses it.
type HotkeyUp struct{ Ref contracts.SessionRef }

// ToggleTalk starts or stops recording regardless of the hotkey mode, e.g.
// from a UI button or a backend without key-up events.
type ToggleTalk struct{ Ref contracts.SessionRef }

// SubmitText is a typed request (ModeText).
type SubmitText struct {
	Ref  contracts.SessionRef
	Text string
}

// Cancel (Escape) ends the active session and everything it started
// (FR-VOICE-006).
type Cancel struct{ Ref contracts.SessionRef }

// TranscriptPartial is an intermediate STT hypothesis, shown but not acted on.
type TranscriptPartial struct {
	Ref  contracts.SessionRef
	Text string
}

// UtteranceEnd is the VAD end-of-speech signal; it stops hands-free recording.
type UtteranceEnd struct{ Ref contracts.SessionRef }

// TranscriptFinal is the final transcript after recording stopped.
type TranscriptFinal struct {
	Ref  contracts.SessionRef
	Text string
}

// CaptureDone hands the screenshots to the Manager, which passes them to
// Steps.Reason or releases them if the generation is gone.
type CaptureDone struct {
	Ref      contracts.SessionRef
	Captures []*contracts.Capture
}

// LLMDelta is a chunk of streamed model output.
type LLMDelta struct {
	Ref  contracts.SessionRef
	Text string
}

// LLMDone ends the model response. Text is the full reply.
type LLMDone struct {
	Ref       contracts.SessionRef
	Text      string
	NeedInput bool
}

// SpeechStarted reports that TTS audio became audible.
type SpeechStarted struct{ Ref contracts.SessionRef }

// SpeechDone reports that TTS playback finished.
type SpeechDone struct{ Ref contracts.SessionRef }

// Failure ends the generation in StateFailed. Op is a technical step name.
type Failure struct {
	Ref contracts.SessionRef
	Op  string
	Err error
}

func (e HotkeyDown) ref() contracts.SessionRef        { return e.Ref }
func (e HotkeyUp) ref() contracts.SessionRef          { return e.Ref }
func (e ToggleTalk) ref() contracts.SessionRef        { return e.Ref }
func (e SubmitText) ref() contracts.SessionRef        { return e.Ref }
func (e Cancel) ref() contracts.SessionRef            { return e.Ref }
func (e TranscriptPartial) ref() contracts.SessionRef { return e.Ref }
func (e UtteranceEnd) ref() contracts.SessionRef      { return e.Ref }
func (e TranscriptFinal) ref() contracts.SessionRef   { return e.Ref }
func (e CaptureDone) ref() contracts.SessionRef       { return e.Ref }
func (e LLMDelta) ref() contracts.SessionRef          { return e.Ref }
func (e LLMDone) ref() contracts.SessionRef           { return e.Ref }
func (e SpeechStarted) ref() contracts.SessionRef     { return e.Ref }
func (e SpeechDone) ref() contracts.SessionRef        { return e.Ref }
func (e Failure) ref() contracts.SessionRef           { return e.Ref }

func (e HotkeyDown) withRef(r contracts.SessionRef) Event        { e.Ref = r; return e }
func (e HotkeyUp) withRef(r contracts.SessionRef) Event          { e.Ref = r; return e }
func (e ToggleTalk) withRef(r contracts.SessionRef) Event        { e.Ref = r; return e }
func (e SubmitText) withRef(r contracts.SessionRef) Event        { e.Ref = r; return e }
func (e Cancel) withRef(r contracts.SessionRef) Event            { e.Ref = r; return e }
func (e TranscriptPartial) withRef(r contracts.SessionRef) Event { e.Ref = r; return e }
func (e UtteranceEnd) withRef(r contracts.SessionRef) Event      { e.Ref = r; return e }
func (e TranscriptFinal) withRef(r contracts.SessionRef) Event   { e.Ref = r; return e }
func (e CaptureDone) withRef(r contracts.SessionRef) Event       { e.Ref = r; return e }
func (e LLMDelta) withRef(r contracts.SessionRef) Event          { e.Ref = r; return e }
func (e LLMDone) withRef(r contracts.SessionRef) Event           { e.Ref = r; return e }
func (e SpeechStarted) withRef(r contracts.SessionRef) Event     { e.Ref = r; return e }
func (e SpeechDone) withRef(r contracts.SessionRef) Event        { e.Ref = r; return e }
func (e Failure) withRef(r contracts.SessionRef) Event           { e.Ref = r; return e }

func (HotkeyDown) userInput() {}
func (HotkeyUp) userInput()   {}
func (ToggleTalk) userInput() {}
func (SubmitText) userInput() {}
func (Cancel) userInput()     {}

// release frees screenshots carried by an event that will not be used.
func release(ev Event) {
	if cd, ok := ev.(CaptureDone); ok {
		for _, c := range cd.Captures {
			if c != nil {
				c.Release()
			}
		}
	}
}

// FromHotkey maps a contracts.HotkeyService event to a Manager input. talk
// is the Talk binding, cancel the Escape binding.
func FromHotkey(ev contracts.HotkeyEvent, talk, cancel contracts.HotkeyID) (Event, bool) {
	switch {
	case ev.ID == talk && ev.Kind == contracts.HotkeyPressed:
		return HotkeyDown{}, true
	case ev.ID == talk && ev.Kind == contracts.HotkeyReleased:
		return HotkeyUp{}, true
	case ev.ID == cancel && ev.Kind == contracts.HotkeyPressed:
		return Cancel{}, true
	}
	return nil, false
}
