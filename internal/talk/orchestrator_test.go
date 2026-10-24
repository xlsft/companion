package talk

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/pkg/contracts"
)

// scriptedProvider returns one scripted event list per Stream call and
// records the requests.
type scriptedProvider struct {
	mu      sync.Mutex
	rounds  [][]llm.Event
	reqs    []llm.ChatRequest
	failErr error
}

func (p *scriptedProvider) Capabilities() llm.Capabilities { return llm.Capabilities{} }

func (p *scriptedProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if p.failErr != nil {
		return nil, p.failErr
	}
	if len(p.rounds) == 0 {
		return &sliceStream{events: []llm.Event{llm.Completed{}}}, nil
	}
	ev := p.rounds[0]
	p.rounds = p.rounds[1:]
	return &sliceStream{events: ev}, nil
}

type sliceStream struct{ events []llm.Event }

func (s *sliceStream) Recv() (llm.Event, error) {
	if len(s.events) == 0 {
		return nil, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}
func (s *sliceStream) Close() error { return nil }

type fakeOverlay struct {
	mu       sync.Mutex
	captures []contracts.CaptureMetadata
	cmds     []overlay.Command
	err      error
}

func (f *fakeOverlay) RegisterCapture(m contracts.CaptureMetadata) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures = append(f.captures, m)
}

func (f *fakeOverlay) Apply(_ context.Context, c overlay.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.cmds = append(f.cmds, c)
	return nil
}

type fakeSpeaker struct {
	mu   sync.Mutex
	said []string
	err  error
}

func (s *fakeSpeaker) Say(_ context.Context, text string, started func()) error {
	started()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, text)
	return s.err
}

type events struct {
	mu  sync.Mutex
	all []session.Event
}

func (e *events) emit(ev session.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.all = append(e.all, ev)
}

func (e *events) kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.all {
		switch ev.(type) {
		case session.LLMDelta:
			out = append(out, "delta")
		case session.SpeechStarted:
			out = append(out, "speech_started")
		case session.SpeechDone:
			out = append(out, "speech_done")
		case session.SpeechFailed:
			out = append(out, "speech_failed")
		}
	}
	return out
}

var ref = contracts.SessionRef{ID: "s1", Generation: 1}

func screenCapture() *contracts.Capture {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	return &contracts.Capture{
		Meta: contracts.CaptureMetadata{
			ID: "cap1", DisplayID: "d1", Native: contracts.PixelSize{W: 3840, H: 2160},
			Bounds: contracts.RectXYWH(0, 0, 1920, 1080), Scale: 2, TopologyVersion: 1,
		},
		Image: img,
	}
}

func TestReasonStreamsTextAndSpeech(t *testing.T) {
	p := &scriptedProvider{rounds: [][]llm.Event{{
		llm.TextDelta{Text: "Open the File menu. "},
		llm.TextDelta{Text: "Then press Export."},
		llm.Completed{StopReason: "end_turn"},
	}}}
	sp := &fakeSpeaker{}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Speaker: sp}
	ev := &events{}
	reply, err := o.Reason(context.Background(), ref, session.Request{Text: "how do I export?"}, ev.emit)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "Open the File menu. Then press Export." {
		t.Fatalf("reply %q", reply.Text)
	}
	if strings.Join(sp.said, "|") != "Open the File menu.|Then press Export." {
		t.Fatalf("spoken %q", sp.said)
	}
	k := strings.Join(ev.kinds(), ",")
	if !strings.HasPrefix(k, "delta") || !strings.HasSuffix(k, "speech_done") || !strings.Contains(k, "speech_started") {
		t.Fatalf("events %s", k)
	}
	if len(p.reqs[0].Tools) != 0 {
		t.Error("tools offered without a screenshot")
	}
}

func TestReasonExecutesPointingTool(t *testing.T) {
	args := json.RawMessage(`{"x":0.5,"y":0.25}`)
	p := &scriptedProvider{rounds: [][]llm.Event{
		{llm.TextDelta{Text: "It's here."}, llm.ToolCallCompleted{ID: "call1", Name: overlay.ToolPointAt, Arguments: args}, llm.Completed{}},
		{llm.TextDelta{Text: " Click it."}, llm.Completed{}},
	}}
	ov := &fakeOverlay{}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: ov}
	shot := screenCapture()
	reply, err := o.Reason(context.Background(), ref, session.Request{Text: "where is export?", Captures: []*contracts.Capture{shot}}, (&events{}).emit)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "It's here. Click it." {
		t.Fatalf("reply %q", reply.Text)
	}
	if shot.Image != nil {
		t.Error("capture not released")
	}
	if len(ov.captures) != 1 || ov.captures[0].Model.W > defaultMaxModelSide || ov.captures[0].Model.W == 0 {
		t.Fatalf("registered capture %+v", ov.captures)
	}
	if len(ov.cmds) != 2 || ov.cmds[0].Capture != "cap1" || ov.cmds[0].Session != ref {
		t.Fatalf("overlay commands %+v", ov.cmds)
	}
	if len(p.reqs) != 2 {
		t.Fatalf("want 2 model rounds, got %d", len(p.reqs))
	}
	first := p.reqs[0]
	if len(first.Tools) == 0 {
		t.Error("pointing tools not offered with a screenshot")
	}
	user := first.Messages[len(first.Messages)-1]
	if len(user.Content) != 2 || user.Content[1].Kind != llm.PartImage {
		t.Fatalf("screenshot not attached: %+v", user.Content)
	}
	second := p.reqs[1].Messages
	last := second[len(second)-1]
	if last.Role != llm.RoleTool || last.ToolCallID != "call1" || last.IsError {
		t.Fatalf("tool result %+v", last)
	}
}

func TestToolErrorsGoBackToTheModel(t *testing.T) {
	args := json.RawMessage(`{"x":0.5,"y":0.5}`)
	p := &scriptedProvider{rounds: [][]llm.Event{
		{llm.ToolCallCompleted{ID: "c", Name: overlay.ToolPointAt, Arguments: args}, llm.Completed{}},
		{llm.TextDelta{Text: "It's at the top right."}, llm.Completed{}},
	}}
	ov := &fakeOverlay{err: contracts.ErrStale}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: ov}
	_, err := o.Reason(context.Background(), ref, session.Request{Text: "x", Captures: []*contracts.Capture{screenCapture()}}, (&events{}).emit)
	if err != nil {
		t.Fatal(err)
	}
	msgs := p.reqs[1].Messages
	if res := msgs[len(msgs)-1]; !res.IsError || !strings.Contains(res.Content[0].Text, "screen changed") {
		t.Fatalf("tool result %+v", res)
	}
}

func TestToolRoundsAreBounded(t *testing.T) {
	call := llm.ToolCallCompleted{ID: "c", Name: overlay.ToolClear, Arguments: json.RawMessage(`{}`)}
	var rounds [][]llm.Event
	for range 10 {
		rounds = append(rounds, []llm.Event{call, llm.Completed{}})
	}
	p := &scriptedProvider{rounds: rounds}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Overlay: &fakeOverlay{}, MaxToolRounds: 3}
	if _, err := o.Reason(context.Background(), ref, session.Request{Text: "x", Captures: []*contracts.Capture{screenCapture()}}, (&events{}).emit); err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 3 {
		t.Fatalf("model called %d times, want 3", len(p.reqs))
	}
}

func TestSpeechFailureKeepsAnswer(t *testing.T) {
	p := &scriptedProvider{rounds: [][]llm.Event{{llm.TextDelta{Text: "Answer."}, llm.Completed{}}}}
	o := &Orchestrator{
		Provider: func(context.Context) (llm.Provider, error) { return p, nil },
		Speaker:  &fakeSpeaker{err: errors.New("tts down")},
	}
	ev := &events{}
	reply, err := o.Reason(context.Background(), ref, session.Request{Text: "q"}, ev.emit)
	if err != nil || reply.Text != "Answer." {
		t.Fatalf("reply %q err %v", reply.Text, err)
	}
	if k := ev.kinds(); k[len(k)-1] != "speech_failed" {
		t.Fatalf("events %v", k)
	}
}

func TestProviderErrorFailsAndReleasesCaptures(t *testing.T) {
	p := &scriptedProvider{failErr: llm.ErrAuth}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }}
	shot := screenCapture()
	_, err := o.Reason(context.Background(), ref, session.Request{Text: "q", Captures: []*contracts.Capture{shot}}, (&events{}).emit)
	if !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("err %v", err)
	}
	if shot.Image != nil {
		t.Error("capture not released")
	}
}

func TestFollowUpCarriesHistory(t *testing.T) {
	p := &scriptedProvider{rounds: [][]llm.Event{
		{llm.TextDelta{Text: "First."}, llm.Completed{}},
		{llm.TextDelta{Text: "Second."}, llm.Completed{}},
		{llm.TextDelta{Text: "Fresh."}, llm.Completed{}},
	}}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }}
	ctx, emit := context.Background(), (&events{}).emit
	_, _ = o.Reason(ctx, ref, session.Request{Text: "one"}, emit)
	_, _ = o.Reason(ctx, contracts.SessionRef{ID: "s1", Generation: 2}, session.Request{Text: "two", FollowUp: true}, emit)
	_, _ = o.Reason(ctx, contracts.SessionRef{ID: "s2", Generation: 3}, session.Request{Text: "three"}, emit)
	if n := len(p.reqs[1].Messages); n != 4 { // system, user one, assistant First., user two
		t.Fatalf("follow-up has %d messages", n)
	}
	if n := len(p.reqs[2].Messages); n != 2 {
		t.Fatalf("new session leaked history: %d messages", n)
	}
}

type fakeMemory struct {
	prepared memory.Prepared
	texts    []string
	recorded [][2]string
}

func (m *fakeMemory) Prepare(_ context.Context, text string) memory.Prepared {
	m.texts = append(m.texts, text)
	return m.prepared
}

func (m *fakeMemory) Record(_ context.Context, _ contracts.SessionRef, user, answer string) {
	m.recorded = append(m.recorded, [2]string{user, answer})
}

// With memory the earlier turns come from it, whether or not the request
// is a follow-up, and the facts block joins the system prompt.
func TestMemoryFeedsContextAndFacts(t *testing.T) {
	hist := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Part{llm.Text("earlier question")}},
		{Role: llm.RoleAssistant, Content: []llm.Part{llm.Text("earlier answer")}},
	}
	tests := []struct {
		name      string
		prepared  memory.Prepared
		prompt    string
		wantMsgs  int
		wantInSys string
	}{
		{"context and facts", memory.Prepared{History: hist, System: "What you know about the user: Anna"}, "", 4, "Anna"},
		{"nothing to add", memory.Prepared{}, "", 2, ""},
		{"custom prompt keeps facts", memory.Prepared{System: "FACTS"}, "custom", 2, "FACTS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &scriptedProvider{rounds: [][]llm.Event{{llm.TextDelta{Text: "Answer."}, llm.Completed{}}}}
			mem := &fakeMemory{prepared: tt.prepared}
			o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Memory: mem, SystemPrompt: tt.prompt}
			if _, err := o.Reason(context.Background(), ref, session.Request{Text: "now"}, (&events{}).emit); err != nil {
				t.Fatal(err)
			}
			msgs := p.reqs[0].Messages
			if len(msgs) != tt.wantMsgs {
				t.Fatalf("%d messages, want %d", len(msgs), tt.wantMsgs)
			}
			if sys := msgs[0].Content[0].Text; !strings.Contains(sys, tt.wantInSys) {
				t.Errorf("system prompt lacks %q", tt.wantInSys)
			}
			if last := msgs[len(msgs)-1]; last.Role != llm.RoleUser || last.Content[0].Text != "now" {
				t.Errorf("last message = %+v", last)
			}
			if len(mem.recorded) != 1 || mem.recorded[0] != [2]string{"now", "Answer."} {
				t.Errorf("recorded %v", mem.recorded)
			}
		})
	}
}

func TestMemoryNotRecordedOnFailure(t *testing.T) {
	p := &scriptedProvider{failErr: errors.New("boom")}
	mem := &fakeMemory{}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Memory: mem}
	if _, err := o.Reason(context.Background(), ref, session.Request{Text: "now"}, (&events{}).emit); err == nil {
		t.Fatal("want error")
	}
	if len(mem.recorded) != 0 {
		t.Errorf("failed turn recorded: %v", mem.recorded)
	}
}

func TestDownscaleKeepsAspect(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 5120, 1440))
	out := downscale(img, 1568).Bounds()
	if out.Dx() != 1568 || out.Dy() != 441 {
		t.Fatalf("got %v", out)
	}
	small := image.NewRGBA(image.Rect(0, 0, 800, 600))
	if downscale(small, 1568) != image.Image(small) {
		t.Fatal("small image was resized")
	}
}

func TestRecorderMissing(t *testing.T) {
	o := &Orchestrator{}
	if err := o.StartRecording(context.Background(), ref, func(session.Event) {}); !errors.Is(err, contracts.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
}
