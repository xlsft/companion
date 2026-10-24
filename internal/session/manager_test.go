package session

import (
	"context"
	"errors"
	"image"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

var errBoom = errors.New("boom")

// act is one scripted input: user inputs are posted as is, results are
// stamped with the active generation.
type act struct {
	ev     Event
	result bool
}

func in(ev Event) act  { return act{ev: ev} }
func res(ev Event) act { return act{ev: ev, result: true} }

func (h *harness) play(acts ...act) {
	h.t.Helper()
	for _, a := range acts {
		if a.result {
			h.postResult(a.ev)
		} else {
			h.post(a.ev)
		}
	}
}

type noRelease struct{}

func (noRelease) SupportsRelease() bool { return false }

func TestTransitionPaths(t *testing.T) {
	tests := []struct {
		name      string
		settings  Settings
		hotkeys   ReleaseReporter
		acts      []act
		want      []State
		wantMode  Mode
		wantCalls []string
	}{
		{
			name:     "hold, capture at end, spoken reply",
			settings: Settings{VoiceMode: ModeHoldToTalk, Capture: CaptureAtEnd, SpeakReplies: true},
			acts: []act{
				in(HotkeyDown{}), in(HotkeyUp{}), res(TranscriptFinal{Text: "open settings"}),
				res(CaptureDone{}), res(LLMDelta{Text: "Click"}), res(LLMDelta{Text: " here"}),
				res(LLMDone{Text: "Click here"}), res(SpeechStarted{}), res(SpeechDone{}),
			},
			want: []State{StateListening, StateTranscribing, StateCapturing, StateReasoning,
				StateResponding, StateResponding, StateSpeaking, StateIdle},
			wantMode:  ModeHoldToTalk,
			wantCalls: []string{"capture", "finish_recording", "reason", "speak", "start_recording"},
		},
		{
			name:     "toggle, no capture",
			settings: Settings{VoiceMode: ModeToggle, Capture: CaptureNever},
			acts: []act{
				in(HotkeyDown{}), in(HotkeyDown{}), res(TranscriptFinal{Text: "hi"}), res(LLMDone{Text: "ok"}),
			},
			want:      []State{StateListening, StateTranscribing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeToggle,
			wantCalls: []string{"finish_recording", "reason", "start_recording"},
		},
		{
			name:     "hands-free ends on VAD",
			settings: Settings{VoiceMode: ModeHandsFree, Capture: CaptureNever},
			acts: []act{
				in(HotkeyDown{}), res(TranscriptPartial{Text: "op"}), res(UtteranceEnd{}),
				res(TranscriptFinal{Text: "open"}), res(LLMDone{}),
			},
			want:      []State{StateListening, StateListening, StateTranscribing, StateReasoning, StateIdle},
			wantMode:  ModeHandsFree,
			wantCalls: []string{"finish_recording", "reason", "start_recording"},
		},
		{
			name:     "hands-free stopped manually",
			settings: Settings{VoiceMode: ModeHandsFree, Capture: CaptureNever},
			acts: []act{
				in(ToggleTalk{}), in(ToggleTalk{}), res(TranscriptFinal{Text: "open"}), res(LLMDone{Text: "a"}),
			},
			want:      []State{StateListening, StateTranscribing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeHandsFree,
			wantCalls: []string{"finish_recording", "reason", "start_recording"},
		},
		{
			name:     "toggle event in hold mode",
			settings: Settings{VoiceMode: ModeHoldToTalk, Capture: CaptureNever},
			acts: []act{
				in(ToggleTalk{}), in(HotkeyUp{}), in(ToggleTalk{}), res(TranscriptFinal{Text: "x"}), res(LLMDone{Text: "a"}),
			},
			want:      []State{StateListening, StateTranscribing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeToggle,
			wantCalls: []string{"finish_recording", "reason", "start_recording"},
		},
		{
			name:     "hold degrades to toggle without key-up",
			settings: Settings{VoiceMode: ModeHoldToTalk, Capture: CaptureNever},
			hotkeys:  noRelease{},
			acts: []act{
				in(HotkeyDown{}), in(HotkeyUp{}), in(HotkeyDown{}), res(TranscriptFinal{Text: "x"}), res(LLMDone{Text: "a"}),
			},
			want:      []State{StateListening, StateTranscribing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeToggle,
			wantCalls: []string{"finish_recording", "reason", "start_recording"},
		},
		{
			name:      "text, capture at end",
			settings:  Settings{Capture: CaptureAtEnd},
			acts:      []act{in(SubmitText{Text: "hi"}), res(CaptureDone{}), res(LLMDone{Text: "a"})},
			want:      []State{StateCapturing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"capture", "reason"},
		},
		{
			name:      "text, capture at start",
			settings:  Settings{Capture: CaptureAtStart},
			acts:      []act{in(SubmitText{Text: "hi"}), res(CaptureDone{}), res(LLMDone{Text: "a"})},
			want:      []State{StateCapturing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"capture", "reason"},
		},
		{
			name:      "text, no capture",
			settings:  Settings{Capture: CaptureNever},
			acts:      []act{in(SubmitText{Text: "hi"}), res(LLMDone{Text: "a"})},
			want:      []State{StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"reason"},
		},
		{
			name:     "capture at start",
			settings: Settings{Capture: CaptureAtStart},
			acts: []act{
				in(HotkeyDown{}), res(CaptureDone{}), in(HotkeyUp{}), res(TranscriptFinal{Text: "x"}), res(LLMDone{Text: "a"}),
			},
			want: []State{StateCapturing, StateListening, StateTranscribing, StateReasoning,
				StateResponding, StateIdle},
			wantMode:  ModeHoldToTalk,
			wantCalls: []string{"capture", "finish_recording", "reason", "start_recording"},
		},
		{
			name:     "capture at start, released while capturing",
			settings: Settings{Capture: CaptureAtStart},
			acts: []act{
				in(HotkeyDown{}), in(HotkeyUp{}), res(CaptureDone{}), res(TranscriptFinal{Text: "x"}), res(LLMDone{Text: "a"}),
			},
			want:      []State{StateCapturing, StateTranscribing, StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeHoldToTalk,
			wantCalls: []string{"capture", "finish_recording", "reason", "start_recording"},
		},
		{
			name:     "speech streamed during response",
			settings: Settings{Capture: CaptureNever, SpeakReplies: true},
			acts: []act{
				in(SubmitText{Text: "hi"}), res(LLMDelta{Text: "a"}), res(SpeechStarted{}),
				res(LLMDelta{Text: "b"}), res(LLMDone{Text: "ab"}), res(SpeechDone{}),
			},
			want:      []State{StateReasoning, StateResponding, StateSpeaking, StateSpeaking, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"reason"},
		},
		{
			name:      "speak without audible start",
			settings:  Settings{Capture: CaptureNever, SpeakReplies: true},
			acts:      []act{in(SubmitText{Text: "hi"}), res(LLMDone{Text: "a"}), res(SpeechDone{})},
			want:      []State{StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"reason", "speak"},
		},
		{
			name:      "empty transcript",
			settings:  Settings{Capture: CaptureNever},
			acts:      []act{in(HotkeyDown{}), in(HotkeyUp{}), res(TranscriptFinal{Text: "  "})},
			want:      []State{StateListening, StateTranscribing, StateIdle},
			wantMode:  ModeHoldToTalk,
			wantCalls: []string{"finish_recording", "start_recording"},
		},
		{
			name:     "awaiting input then follow-up",
			settings: Settings{Capture: CaptureNever},
			acts: []act{
				in(SubmitText{Text: "hi"}), res(LLMDone{Text: "which one?", NeedInput: true}),
				in(SubmitText{Text: "left"}), res(LLMDone{Text: "ok"}),
			},
			want: []State{StateReasoning, StateResponding, StateAwaitingInput,
				StateReasoning, StateResponding, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"reason", "reason"},
		},
		{
			name:      "failure",
			settings:  Settings{Capture: CaptureNever},
			acts:      []act{in(SubmitText{Text: "hi"}), res(Failure{Op: "reason", Err: errBoom})},
			want:      []State{StateReasoning, StateFailed, StateIdle},
			wantMode:  ModeText,
			wantCalls: []string{"reason"},
		},
		{
			name:      "cancel",
			settings:  Settings{},
			acts:      []act{in(HotkeyDown{}), in(Cancel{})},
			want:      []State{StateListening, StateCancelled, StateIdle},
			wantMode:  ModeHoldToTalk,
			wantCalls: []string{"start_recording"},
		},
		{
			name:     "barge-in while speaking",
			settings: Settings{Capture: CaptureNever, SpeakReplies: true},
			acts: []act{
				in(SubmitText{Text: "hi"}), res(LLMDone{Text: "a"}), res(SpeechStarted{}), in(SubmitText{Text: "stop"}),
			},
			want:      []State{StateReasoning, StateResponding, StateSpeaking, StateInterrupted, StateReasoning},
			wantMode:  ModeText,
			wantCalls: []string{"reason", "reason", "speak"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, Config{Settings: tt.settings, Hotkeys: tt.hotkeys})
			h.play(tt.acts...)
			snaps := h.expect(tt.want...)
			h.expectNone()
			if got := h.allCalls(len(tt.wantCalls)); !reflect.DeepEqual(got, tt.wantCalls) {
				t.Errorf("calls %v, want %v", got, tt.wantCalls)
			}
			id := snaps[0].Ref.ID
			var lastGen uint64
			for _, s := range snaps {
				if s.Mode != tt.wantMode {
					t.Errorf("%v: mode %v, want %v", s.State, s.Mode, tt.wantMode)
				}
				if s.Ref.ID != id {
					t.Errorf("%v: session %q, want %q", s.State, s.Ref.ID, id)
				}
				if s.Ref.Generation < lastGen {
					t.Errorf("%v: generation went back to %d", s.State, s.Ref.Generation)
				}
				lastGen = s.Ref.Generation
			}
			if st := h.m.Stats(); st.Dropped != 0 {
				t.Errorf("dropped %d events", st.Dropped)
			}
		})
	}
}

func TestSnapshotDetails(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever}})
	h.play(in(SubmitText{Text: "hi"}), res(LLMDelta{Text: "a"}), res(LLMDelta{Text: "b"}),
		res(Failure{Op: "reason", Err: errBoom}))
	s := h.expect(StateReasoning, StateResponding, StateResponding, StateFailed, StateIdle)
	if s[1].Text != "a" || s[2].Text != "b" {
		t.Errorf("deltas %q %q", s[1].Text, s[2].Text)
	}
	if !s[2].Since.Equal(s[1].Since) {
		t.Error("text update changed Since")
	}
	if s[3].Reason != "reason" || !errors.Is(s[3].Err, errBoom) {
		t.Errorf("failed snapshot %+v", s[3])
	}
	if h.m.Current().State != StateIdle {
		t.Errorf("current %v", h.m.Current().State)
	}
}

func TestFollowUpKeepsSessionID(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever, SpeakReplies: true}})
	h.post(SubmitText{Text: "hi"})
	first := h.expect(StateReasoning)[0]
	r1 := h.waitCall("reason")
	h.postResult(LLMDelta{Text: "a"})
	h.post(HotkeyDown{}) // barge-in
	s := h.expect(StateResponding, StateInterrupted, StateListening)
	if s[1].Ref != first.Ref || s[1].Reason != "barge_in" {
		t.Errorf("interrupted %+v, want ref %v", s[1], first.Ref)
	}
	second := s[2].Ref
	if second.ID != first.Ref.ID || second.Generation <= first.Ref.Generation {
		t.Errorf("barge-in ref %v after %v", second, first.Ref)
	}
	waitDone(t, r1.ctx, 50*time.Millisecond)
	if !errors.Is(context.Cause(r1.ctx), ErrInterrupted) {
		t.Errorf("cause %v", context.Cause(r1.ctx))
	}
	h.post(HotkeyUp{})
	h.postResult(TranscriptFinal{Text: "x"})
	r2 := h.waitCall("reason")
	if !r2.req.FollowUp || r2.req.Text != "x" || r2.ref != second {
		t.Errorf("follow-up request %+v ref %v", r2.req, r2.ref)
	}
	h.post(Cancel{})
	h.expect(StateTranscribing, StateReasoning, StateCancelled, StateIdle)

	// A request from Idle starts a new session.
	h.post(SubmitText{Text: "again"})
	if got := h.expect(StateReasoning)[0].Ref; got.ID == first.Ref.ID {
		t.Errorf("new request reused session %q", got.ID)
	}
	if r := h.waitCall("reason"); r.req.FollowUp {
		t.Error("new session marked as follow-up")
	}
}

// reach drives a fresh harness into state and returns the generation ctx.
func reach(t *testing.T, state State) (*harness, context.Context) {
	t.Helper()
	settings := Settings{Capture: CaptureAtEnd, SpeakReplies: true}
	h := newHarness(t, Config{Settings: settings})
	h.post(HotkeyDown{})
	ctx := h.waitCall("start_recording").ctx
	path := []struct {
		state State
		act   act
	}{
		{StateListening, act{}},
		{StateTranscribing, in(HotkeyUp{})},
		{StateCapturing, res(TranscriptFinal{Text: "x"})},
		{StateReasoning, res(CaptureDone{})},
		{StateResponding, res(LLMDelta{Text: "a"})},
		{StateSpeaking, res(SpeechStarted{})},
	}
	for _, p := range path {
		if p.act.ev != nil {
			h.play(p.act)
		}
		h.expect(p.state)
		if p.state == state {
			return h, ctx
		}
	}
	if state != StateAwaitingInput {
		t.Fatalf("cannot reach %v", state)
	}
	h.play(res(LLMDone{Text: "which?", NeedInput: true}), res(SpeechDone{}))
	h.expect(StateAwaitingInput)
	return h, ctx
}

var workingStates = []State{StateListening, StateTranscribing, StateCapturing, StateReasoning,
	StateResponding, StateSpeaking, StateAwaitingInput}

func TestCancelFromEveryWorkingState(t *testing.T) {
	for _, st := range workingStates {
		t.Run(string(st), func(t *testing.T) {
			h, ctx := reach(t, st)
			ref := h.m.Current().Ref
			start := time.Now()
			if err := h.m.Post(Cancel{}); err != nil {
				t.Fatal(err)
			}
			waitDone(t, ctx, 50*time.Millisecond)
			if d := time.Since(start); d > 50*time.Millisecond {
				t.Errorf("cancel took %v", d)
			}
			if !errors.Is(context.Cause(ctx), ErrCancelled) {
				t.Errorf("cause %v", context.Cause(ctx))
			}
			s := h.expect(StateCancelled, StateIdle)
			if s[0].Ref != ref || s[0].Reason != "cancel" {
				t.Errorf("cancelled snapshot %+v", s[0])
			}
			// Late results of the cancelled generation never reach observers.
			h.post(LLMDelta{Ref: ref, Text: "late"})
			h.post(SpeechStarted{Ref: ref})
			h.expectNone()
			if got := h.m.Stats().Dropped; got != 2 {
				t.Errorf("dropped %d, want 2", got)
			}
		})
	}
}

func TestIllegalEventsIgnored(t *testing.T) {
	tests := []struct {
		state   State
		illegal []act
	}{
		{StateListening, []act{in(HotkeyDown{}), in(SubmitText{Text: "x"}), res(UtteranceEnd{}),
			res(TranscriptFinal{Text: "x"}), res(CaptureDone{}), res(LLMDelta{}), res(LLMDone{}),
			res(SpeechStarted{}), res(SpeechDone{})}},
		{StateTranscribing, []act{in(HotkeyDown{}), in(HotkeyUp{}), in(ToggleTalk{}), in(SubmitText{Text: "x"}),
			res(CaptureDone{}), res(LLMDelta{}), res(LLMDone{}), res(UtteranceEnd{})}},
		{StateCapturing, []act{in(HotkeyDown{}), in(HotkeyUp{}), res(TranscriptFinal{Text: "x"}),
			res(TranscriptPartial{Text: "x"}), res(LLMDelta{}), res(SpeechDone{})}},
		{StateReasoning, []act{in(HotkeyUp{}), res(TranscriptFinal{Text: "x"}), res(CaptureDone{}),
			res(SpeechDone{}), res(UtteranceEnd{})}},
		{StateResponding, []act{in(HotkeyUp{}), res(TranscriptFinal{Text: "x"}), res(CaptureDone{}),
			res(SpeechDone{})}},
		{StateSpeaking, []act{res(SpeechStarted{}), res(TranscriptFinal{Text: "x"}), res(CaptureDone{})}},
		{StateAwaitingInput, []act{in(HotkeyUp{}), in(SubmitText{Text: " "}), res(LLMDelta{}), res(LLMDone{}),
			res(SpeechStarted{}), res(SpeechDone{}), res(CaptureDone{})}},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			h, ctx := reach(t, tt.state)
			before := h.m.Current()
			h.play(tt.illegal...)
			h.expectNone()
			if got := h.m.Current(); got.State != before.State || got.Ref != before.Ref {
				t.Errorf("state changed to %+v", got)
			}
			if st := h.m.Stats(); st.Ignored != uint64(len(tt.illegal)) || st.Dropped != 0 {
				t.Errorf("stats %+v, want %d ignored", st, len(tt.illegal))
			}
			if ctx.Err() != nil {
				t.Error("context cancelled by illegal event")
			}
			// The machine still works.
			h.post(Cancel{})
			h.expect(StateCancelled, StateIdle)
		})
	}

	t.Run(string(StateIdle), func(t *testing.T) {
		h := newHarness(t, Config{})
		h.play(in(HotkeyUp{}), in(Cancel{}), in(SubmitText{Text: "  "}))
		h.expectNone()
		if st := h.m.Stats(); st.Ignored != 3 {
			t.Errorf("stats %+v", st)
		}
		h.post(HotkeyDown{})
		h.expect(StateListening)
	})
}

func TestStaleGenerationDropped(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever}})
	h.post(SubmitText{Text: "one"})
	old := h.expect(StateReasoning)[0].Ref
	h.post(Cancel{})
	h.expect(StateCancelled, StateIdle)
	h.post(SubmitText{Text: "two"})
	cur := h.expect(StateReasoning)[0].Ref

	img := &contracts.Capture{Image: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	h.play(
		in(LLMDelta{Ref: old, Text: "late"}),
		in(LLMDone{Ref: old, Text: "late"}),
		in(TranscriptFinal{Ref: old, Text: "late"}),
		in(CaptureDone{Ref: old, Captures: []*contracts.Capture{img}}),
		in(Cancel{Ref: old}),       // targeted user input of an old generation
		in(LLMDelta{Text: "zero"}), // results must be stamped
	)
	h.expectNone()
	if st := h.m.Stats(); st.Dropped != 6 || st.Ignored != 0 {
		t.Errorf("stats %+v, want 6 dropped", st)
	}
	if img.Image != nil {
		t.Error("stale capture not released")
	}
	if got := h.m.Current(); got.Ref != cur || got.State != StateReasoning {
		t.Errorf("current %+v", got)
	}
	h.post(Cancel{Ref: cur})
	h.expect(StateCancelled, StateIdle)
}

func TestCapturesReleasedOnCancel(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureAtStart}})
	h.post(HotkeyDown{})
	h.expect(StateCapturing)
	img := &contracts.Capture{Image: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	h.postResult(CaptureDone{Captures: []*contracts.Capture{img}})
	h.expect(StateListening)
	h.post(Cancel{})
	h.expect(StateCancelled, StateIdle)
	if img.Image != nil {
		t.Error("capture of cancelled generation not released")
	}
}

func TestSettingsApplyToNextGeneration(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever}})
	h.post(HotkeyDown{})
	h.expect(StateListening)
	if err := h.m.SetSettings(Settings{VoiceMode: ModeToggle, Capture: CaptureAtEnd}); err != nil {
		t.Fatal(err)
	}
	h.play(in(HotkeyUp{}), res(TranscriptFinal{Text: "x"}))
	h.expect(StateTranscribing, StateReasoning) // old settings: hold, no capture
	h.post(Cancel{})
	h.expect(StateCancelled, StateIdle)
	h.play(in(HotkeyDown{}), in(HotkeyUp{}))
	if s := h.expect(StateListening)[0]; s.Mode != ModeToggle {
		t.Errorf("mode %v", s.Mode)
	}
	h.expectNone()
}

// autoSteps returns results directly, exercising the step → event path.
type autoSteps struct {
	mu         sync.Mutex
	req        Request
	captureErr error
	capture    *contracts.Capture
}

func (s *autoSteps) StartRecording(ctx context.Context, ref contracts.SessionRef, emit Emitter) error {
	emit(TranscriptPartial{Text: "op"})
	return nil
}

func (s *autoSteps) FinishRecording(ctx context.Context, ref contracts.SessionRef) (string, error) {
	return "open settings", nil
}

func (s *autoSteps) Capture(ctx context.Context, ref contracts.SessionRef) ([]*contracts.Capture, error) {
	return []*contracts.Capture{s.capture}, s.captureErr
}

func (s *autoSteps) Reason(ctx context.Context, ref contracts.SessionRef, req Request, emit Emitter) (Reply, error) {
	s.mu.Lock()
	s.req = req
	s.mu.Unlock()
	emit(LLMDelta{Text: "Click "})
	emit(LLMDelta{Text: "here"})
	return Reply{Text: "Click here"}, nil
}

func (s *autoSteps) Speak(ctx context.Context, ref contracts.SessionRef, text string, emit Emitter) error {
	emit(SpeechStarted{})
	return nil
}

func TestStepResultsDriveSession(t *testing.T) {
	shot := &contracts.Capture{Image: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	steps := &autoSteps{capture: shot}
	h := newHarnessWith(t, steps, Config{Settings: Settings{SpeakReplies: true}})
	h.post(HotkeyDown{})
	h.expect(StateListening, StateListening) // state, then partial
	h.post(HotkeyUp{})
	h.expect(StateTranscribing, StateCapturing, StateReasoning, StateResponding, StateResponding,
		StateSpeaking, StateIdle)
	steps.mu.Lock()
	defer steps.mu.Unlock()
	if steps.req.Text != "open settings" || len(steps.req.Captures) != 1 || steps.req.Captures[0] != shot {
		t.Errorf("request %+v", steps.req)
	}
	if shot.Image == nil {
		t.Error("capture handed to Reason was released by the manager")
	}
}

func TestStepErrorFails(t *testing.T) {
	shot := &contracts.Capture{Image: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	steps := &autoSteps{capture: shot, captureErr: contracts.PermissionDenied("screen_capture")}
	h := newHarnessWith(t, steps, Config{})
	h.post(SubmitText{Text: "hi"})
	s := h.expect(StateCapturing, StateFailed, StateIdle)
	if s[1].Reason != "capture" || !errors.Is(s[1].Err, contracts.ErrPermissionDenied) {
		t.Errorf("failed snapshot %+v", s[1])
	}
	if shot.Image != nil {
		t.Error("capture returned with an error not released")
	}
}

func TestClose(t *testing.T) {
	h := newHarness(t, Config{})
	h.post(HotkeyDown{})
	ctx := h.waitCall("start_recording").ctx
	h.expect(StateListening)
	if err := h.m.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, ctx, 50*time.Millisecond)
	if !errors.Is(context.Cause(ctx), ErrClosed) {
		t.Errorf("cause %v", context.Cause(ctx))
	}
	if err := h.m.Post(Cancel{}); !errors.Is(err, ErrClosed) {
		t.Errorf("post after close: %v", err)
	}
	if err := h.m.SetSettings(Settings{}); !errors.Is(err, ErrClosed) {
		t.Errorf("settings after close: %v", err)
	}
	if _, ok := <-h.snaps; ok {
		t.Error("subscription not closed")
	}
	ch, unsub := h.m.Subscribe()
	unsub()
	if _, ok := <-ch; ok {
		t.Error("subscribe after close returned an open channel")
	}
	checkNoLeaks(t)
}

func TestUnsubscribe(t *testing.T) {
	h := newHarness(t, Config{})
	ch, unsub := h.m.Subscribe()
	if s := <-ch; s.State != StateIdle {
		t.Fatalf("first snapshot %v", s.State)
	}
	unsub()
	h.post(HotkeyDown{})
	h.expect(StateListening)
	for range ch { // drained and closed
	}
}

// TestNoOldGenerationAfterNewer races late results of old generations
// against barge-ins and checks what an observer sees (US-10).
func TestNoOldGenerationAfterNewer(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever}})
	var wg sync.WaitGroup
	refs := make(chan contracts.SessionRef, 256)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range refs {
				for range 10 {
					_ = h.m.Post(LLMDelta{Ref: r, Text: "late"})
					_ = h.m.Post(LLMDone{Ref: r, Text: "late"})
				}
			}
		}()
	}
	const rounds = 50
	for i := range rounds {
		h.post(SubmitText{Text: "q"})
		r := h.m.Current().Ref
		refs <- r
		if i%2 == 0 {
			h.post(Cancel{})
		}
	}
	close(refs)
	wg.Wait()
	h.post(Cancel{})
	h.sync()

	var lastGen uint64
	ended := map[uint64]bool{}
	for {
		var s Snapshot
		select {
		case s = <-h.snaps:
		case <-time.After(50 * time.Millisecond):
			return
		}
		g := s.Ref.Generation
		if g < lastGen {
			t.Fatalf("generation %d published after %d", g, lastGen)
		}
		lastGen = g
		if ended[g] && s.State != StateIdle {
			t.Fatalf("generation %d published %v after it ended", g, s.State)
		}
		switch s.State {
		case StateCancelled, StateInterrupted, StateFailed, StateIdle:
			ended[g] = true
		}
	}
}

func TestFromHotkey(t *testing.T) {
	tests := []struct {
		ev   contracts.HotkeyEvent
		want Event
	}{
		{contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyPressed}, HotkeyDown{}},
		{contracts.HotkeyEvent{ID: "talk", Kind: contracts.HotkeyReleased}, HotkeyUp{}},
		{contracts.HotkeyEvent{ID: "cancel", Kind: contracts.HotkeyPressed}, Cancel{}},
		{contracts.HotkeyEvent{ID: "cancel", Kind: contracts.HotkeyReleased}, nil},
		{contracts.HotkeyEvent{ID: "dictate", Kind: contracts.HotkeyPressed}, nil},
	}
	for _, tt := range tests {
		got, ok := FromHotkey(tt.ev, "talk", "cancel")
		if ok != (tt.want != nil) || got != tt.want {
			t.Errorf("FromHotkey(%+v) = %v, %v; want %v", tt.ev, got, ok, tt.want)
		}
	}
}

func TestSpeakFailureKeepsTextAnswer(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever, SpeakReplies: true}})
	h.post(SubmitText{Text: "hi"})
	h.expect(StateReasoning)
	h.waitCall("reason")
	h.postResult(LLMDone{Text: "answer"})
	h.expect(StateResponding)
	speak := h.waitCall("speak")
	ttsErr := errors.New("tts unavailable")
	h.postResult(Failure{Op: opSpeak, Err: ttsErr})
	s := h.expect(StateIdle)[0]
	if s.Reason != "done_without_speech" || !errors.Is(s.Err, ttsErr) {
		t.Fatalf("final snapshot %+v", s)
	}
	waitDone(t, speak.ctx, 50*time.Millisecond)
	if !errors.Is(context.Cause(speak.ctx), ErrDone) {
		t.Errorf("cause %v, want ErrDone", context.Cause(speak.ctx))
	}
}

func TestStreamedSpeechFailureDegradesToText(t *testing.T) {
	h := newHarness(t, Config{Settings: Settings{Capture: CaptureNever, SpeakReplies: true}})
	h.post(SubmitText{Text: "hi"})
	h.expect(StateReasoning)
	h.waitCall("reason")
	h.postResult(LLMDelta{Text: "part"})
	h.postResult(SpeechStarted{})
	h.expect(StateResponding, StateSpeaking)
	h.postResult(SpeechFailed{Err: errors.New("device lost")})
	h.expect(StateResponding)
	h.postResult(LLMDone{Text: "part of the answer"})
	if s := h.expect(StateIdle)[0]; s.Reason != "done_without_speech" {
		t.Fatalf("final snapshot %+v", s)
	}
	// The manager must not try to speak the reply again after a failure.
	h.expectNone()
}
