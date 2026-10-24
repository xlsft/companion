package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/history"
	"github.com/xlsft/companion/internal/memory"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/pkg/contracts"
)

const wait = 2 * time.Second

// fakeTalk is a session the test drives snapshot by snapshot.
type fakeTalk struct {
	snaps chan session.Snapshot
	mu    sync.Mutex
	posts []session.Event
}

func newFakeTalk() *fakeTalk { return &fakeTalk{snaps: make(chan session.Snapshot, 64)} }

func (f *fakeTalk) Post(ev session.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, ev)
	return nil
}

func (f *fakeTalk) Subscribe() (<-chan session.Snapshot, func()) { return f.snaps, func() {} }

func (f *fakeTalk) posted() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b []string
	for _, ev := range f.posts {
		b = append(b, fmt.Sprintf("%T", ev))
	}
	return strings.Join(b, ",")
}

// fakeStore is an in-memory history.
type fakeStore struct {
	mu    sync.Mutex
	convs []*history.Conversation
}

func (f *fakeStore) List(_ context.Context, p history.Page) ([]history.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []history.Conversation
	for i := len(f.convs) - 1; i >= 0 && len(out) < p.Limit; i-- {
		out = append(out, history.Conversation{ID: f.convs[i].ID, Title: f.convs[i].Title})
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*history.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.convs {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, history.ErrNotFound
}

func (f *fakeStore) CreateConversation(_ context.Context, title, _ string) (*history.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &history.Conversation{ID: fmt.Sprintf("c%d", len(f.convs)+1), Title: title}
	f.convs = append(f.convs, c)
	return c, nil
}

func (f *fakeStore) AppendMessage(_ context.Context, id string, m history.Message, _ []contracts.CaptureMetadata, _ map[contracts.CaptureID]*image.RGBA) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.convs {
		if c.ID == id {
			c.Messages = append(c.Messages, m)
			return "m", nil
		}
	}
	return "", history.ErrNotFound
}

func (f *fakeStore) SetKeepScreenshots(context.Context, string, bool) error { return nil }

func (f *fakeStore) messages() []history.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []history.Message
	for _, c := range f.convs {
		out = append(out, c.Messages...)
	}
	return out
}

var (
	g1 = contracts.SessionRef{ID: "s", Generation: 1}
	g2 = contracts.SessionRef{ID: "s", Generation: 2}
)

func snap(ref contracts.SessionRef, st session.State, mode session.Mode, text, request string) session.Snapshot {
	return session.Snapshot{Ref: ref, State: st, Mode: mode, Text: text, Request: request}
}

// describe renders messages compactly for comparison.
func describe(ms []Message) string {
	var b []string
	for _, m := range ms {
		s := "A:"
		if m.Role == RoleUser {
			s = "U:"
			if m.Via == ViaVoice {
				s += "voice(" + m.STTModel + "):"
			}
		}
		s += m.Text
		switch {
		case m.Streaming:
			s += "…"
		case m.Interrupted:
			s += "[interrupted]"
		case m.Failed:
			s += "[failed " + m.Code + "]"
		}
		b = append(b, s)
	}
	return strings.Join(b, " | ")
}

// settle waits for the view in which want holds.
func settle(t *testing.T, ch <-chan View, want func(View) bool) View {
	t.Helper()
	deadline := time.After(wait)
	var last View
	for {
		select {
		case v := <-ch:
			last = v
			if want(v) {
				return v
			}
		case <-deadline:
			t.Fatalf("view never settled; last: phase=%v messages=%q", last.Phase, describe(last.Messages))
		}
	}
}

func start(t *testing.T, cfg Config) (*Service, *fakeTalk, <-chan View) {
	t.Helper()
	ft := newFakeTalk()
	cfg.Talk = ft
	s := New(cfg)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	ch, _ := s.Subscribe()
	return s, ft, ch
}

func TestTranscript(t *testing.T) {
	text, voice := session.ModeText, session.ModeToggle
	tests := []struct {
		name  string
		snaps []session.Snapshot
		phase Phase
		want  string
	}{
		{
			name: "text request streams and finishes",
			snaps: []session.Snapshot{
				snap(g1, session.StateReasoning, text, "", "hello"),
				snap(g1, session.StateResponding, text, "Hi", ""),
				snap(g1, session.StateResponding, text, " there", ""),
				snap(g1, session.StateIdle, text, "", ""),
			},
			phase: PhaseIdle,
			want:  "U:hello | A:Hi there",
		},
		{
			name: "streaming reply stays open",
			snaps: []session.Snapshot{
				snap(g1, session.StateReasoning, text, "", "hello"),
				snap(g1, session.StateSpeaking, text, "Hi", ""),
			},
			phase: PhaseSpeaking,
			want:  "U:hello | A:Hi…",
		},
		{
			name: "voice request carries the recogniser",
			snaps: []session.Snapshot{
				snap(g1, session.StateListening, voice, "spo", ""),
				snap(g1, session.StateTranscribing, voice, "", ""),
				snap(g1, session.StateCapturing, voice, "", "spoken"),
				snap(g1, session.StateResponding, voice, "ok", ""),
				snap(g1, session.StateIdle, voice, "", ""),
			},
			phase: PhaseIdle,
			want:  "U:voice(whisper medium):spoken | A:ok",
		},
		{
			name: "stop marks the reply interrupted",
			snaps: []session.Snapshot{
				snap(g1, session.StateReasoning, text, "", "q"),
				snap(g1, session.StateResponding, text, "partial", ""),
				snap(g1, session.StateCancelled, text, "", ""),
				snap(g1, session.StateIdle, text, "", ""),
			},
			phase: PhaseIdle,
			want:  "U:q | A:partial[interrupted]",
		},
		{
			name: "barge-in starts a new turn",
			snaps: []session.Snapshot{
				snap(g1, session.StateReasoning, text, "", "q1"),
				snap(g1, session.StateResponding, text, "a1", ""),
				snap(g1, session.StateInterrupted, text, "", ""),
				snap(g2, session.StateReasoning, text, "", "q2"),
				snap(g2, session.StateResponding, text, "a2", ""),
				snap(g2, session.StateIdle, text, "", ""),
			},
			phase: PhaseIdle,
			want:  "U:q1 | A:a1[interrupted] | U:q2 | A:a2",
		},
		{
			name: "failure before any output is shown",
			snaps: []session.Snapshot{
				snap(g1, session.StateReasoning, text, "", "q"),
				{Ref: g1, State: session.StateFailed, Mode: text, Reason: "reason"},
				snap(g1, session.StateIdle, text, "", ""),
			},
			phase: PhaseIdle,
			want:  "U:q | A:[failed reason]",
		},
		{
			name: "empty transcript adds nothing",
			snaps: []session.Snapshot{
				snap(g1, session.StateListening, voice, "", ""),
				snap(g1, session.StateTranscribing, voice, "", ""),
				{Ref: g1, State: session.StateIdle, Mode: voice, Reason: "empty_transcript"},
			},
			phase: PhaseIdle,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ft, ch := start(t, Config{STTModel: func() string { return "whisper medium" }})
			for _, s := range tt.snaps {
				ft.snaps <- s
			}
			settle(t, ch, func(v View) bool { return v.Phase == tt.phase && describe(v.Messages) == tt.want })
		})
	}
}

func TestPartialTranscriptWhileListening(t *testing.T) {
	_, ft, ch := start(t, Config{})
	ft.snaps <- snap(g1, session.StateListening, session.ModeToggle, "hel", "")
	v := settle(t, ch, func(v View) bool { return v.Partial == "hel" })
	if v.Phase != PhaseListening {
		t.Errorf("phase %v", v.Phase)
	}
	ft.snaps <- snap(g1, session.StateCapturing, session.ModeToggle, "", "hello")
	settle(t, ch, func(v View) bool { return v.Partial == "" && describe(v.Messages) == "U:voice():hello" })
}

func TestControls(t *testing.T) {
	tests := []struct {
		name    string
		voice   bool
		do      func(context.Context, *Service) error
		wantErr error
		posted  string
	}{
		{"send", false, func(ctx context.Context, s *Service) error { return s.Send(ctx, "  hi ") }, nil, "session.SubmitText"},
		{"send blank", false, func(ctx context.Context, s *Service) error { return s.Send(ctx, "  ") }, nil, ""},
		{"stop", false, func(ctx context.Context, s *Service) error { return s.Stop(ctx) }, nil, "session.Cancel"},
		{"voice", true, func(ctx context.Context, s *Service) error { return s.ToggleVoice(ctx) }, nil, "session.ToggleTalk"},
		{"voice unavailable", false, func(ctx context.Context, s *Service) error { return s.ToggleVoice(ctx) }, contracts.ErrUnsupported, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ft, _ := start(t, Config{Caps: func() Capabilities { return Capabilities{Voice: tt.voice} }})
			err := tt.do(context.Background(), s)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := ft.posted(); got != tt.posted {
				t.Errorf("posted %q, want %q", got, tt.posted)
			}
		})
	}
}

func TestCapabilitiesTextNeedsTalk(t *testing.T) {
	s := New(Config{Caps: func() Capabilities { return Capabilities{Text: false, Model: "m"} }, Talk: newFakeTalk()})
	if c := s.Capabilities(); !c.Text || c.Model != "m" {
		t.Errorf("caps %+v", c)
	}
}

func TestCancelledContext(t *testing.T) {
	s, ft, _ := start(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v", err)
	}
	if ft.posted() != "" {
		t.Error("posted with a cancelled context")
	}
}

func TestNewChatDropsLateSnapshots(t *testing.T) {
	s, ft, ch := start(t, Config{})
	ft.snaps <- snap(g1, session.StateReasoning, session.ModeText, "", "q")
	ft.snaps <- snap(g1, session.StateResponding, session.ModeText, "a", "")
	settle(t, ch, func(v View) bool { return describe(v.Messages) == "U:q | A:a…" })
	if err := s.NewChat(context.Background()); err != nil {
		t.Fatal(err)
	}
	ft.snaps <- snap(g1, session.StateResponding, session.ModeText, "late", "")
	ft.snaps <- snap(g1, session.StateCancelled, session.ModeText, "", "")
	ft.snaps <- snap(g1, session.StateIdle, session.ModeText, "", "")
	v := settle(t, ch, func(v View) bool { return v.Phase == PhaseIdle })
	if len(v.Messages) != 0 || v.Title != "" {
		t.Errorf("new chat shows %q (title %q)", describe(v.Messages), v.Title)
	}
	if !strings.Contains(ft.posted(), "session.Cancel") {
		t.Error("new chat did not cancel the running request")
	}
}

func TestHistory(t *testing.T) {
	tests := []struct {
		name   string
		save   bool
		stored int
	}{
		{"saving on", true, 2},
		{"saving off", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			_, ft, ch := start(t, Config{History: st, Save: func() bool { return tt.save }, STTModel: func() string { return "whisper small" }})
			ft.snaps <- snap(g1, session.StateCapturing, session.ModeToggle, "", "spoken question")
			ft.snaps <- snap(g1, session.StateResponding, session.ModeToggle, "answer", "")
			ft.snaps <- snap(g1, session.StateIdle, session.ModeToggle, "", "")
			v := settle(t, ch, func(v View) bool { return v.Phase == PhaseIdle && len(v.Messages) == 2 })
			if v.Title != "spoken question" {
				t.Errorf("title %q", v.Title)
			}
			deadline := time.Now().Add(wait)
			for len(st.messages()) < tt.stored && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(10 * time.Millisecond)
			if got := len(st.messages()); got != tt.stored {
				t.Fatalf("stored %d messages, want %d", got, tt.stored)
			}
			if tt.stored == 0 {
				return
			}
			// A new run shows the stored conversation again.
			_, _, ch2 := start(t, Config{History: st})
			v2 := settle(t, ch2, func(v View) bool { return len(v.Messages) == 2 })
			if got := describe(v2.Messages); got != "U:voice(whisper small):spoken question | A:answer" || v2.Title != "spoken question" {
				t.Errorf("reloaded %q title %q", got, v2.Title)
			}
		})
	}
}

func TestTitleOf(t *testing.T) {
	tests := []struct{ in, want string }{
		{"short", "short"},
		{"  spaced \n out  ", "spaced out"},
		{strings.Repeat("я", 60), strings.Repeat("я", titleRunes-1) + "…"},
	}
	for _, tt := range tests {
		if got := titleOf(tt.in); got != tt.want {
			t.Errorf("titleOf(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Nothing the user said or the model answered may reach the log.
func TestNoContentLogged(t *testing.T) {
	var buf syncBuf
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := &failingStore{}
	_, ft, ch := start(t, Config{History: st, Save: func() bool { return true }, Log: log})
	ft.snaps <- snap(g1, session.StateReasoning, session.ModeText, "", "secret question")
	ft.snaps <- snap(g1, session.StateResponding, session.ModeText, "secret answer", "")
	ft.snaps <- snap(g1, session.StateIdle, session.ModeText, "", "")
	settle(t, ch, func(v View) bool { return v.Phase == PhaseIdle && len(v.Messages) == 2 })
	deadline := time.Now().Add(wait)
	for !strings.Contains(buf.String(), "create conversation failed") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if out := buf.String(); strings.Contains(out, "secret") || !strings.Contains(out, "failed") {
		t.Errorf("log: %s", out)
	}
}

type failingStore struct{ fakeStore }

func (*failingStore) CreateConversation(context.Context, string, string) (*history.Conversation, error) {
	return nil, errors.New("disk full")
}

// The real session Manager publishes what the chat needs.
func TestWithSessionManager(t *testing.T) {
	m := session.New(replySteps{}, session.Config{Settings: session.Settings{Capture: session.CaptureNever}})
	defer m.Close()
	s := New(Config{Talk: m})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch, _ := s.Subscribe()
	if err := s.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	settle(t, ch, func(v View) bool { return v.Phase == PhaseIdle && describe(v.Messages) == "U:ping | A:po-ng" })
}

type replySteps struct{}

func (replySteps) StartRecording(context.Context, contracts.SessionRef, session.Emitter) error {
	return contracts.Unsupported(contracts.CapMicrophone)
}
func (replySteps) FinishRecording(context.Context, contracts.SessionRef) (string, error) {
	return "", nil
}
func (replySteps) Capture(context.Context, contracts.SessionRef) ([]*contracts.Capture, error) {
	return nil, nil
}
func (replySteps) Reason(_ context.Context, _ contracts.SessionRef, _ session.Request, emit session.Emitter) (session.Reply, error) {
	emit(session.LLMDelta{Text: "po"})
	emit(session.LLMDelta{Text: "-ng"})
	return session.Reply{Text: "po-ng"}, nil
}
func (replySteps) Speak(context.Context, contracts.SessionRef, string, session.Emitter) error {
	return nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type fakeContext struct {
	mu     sync.Mutex
	seeded [][]memory.Turn
	resets int
}

func (f *fakeContext) Seed(t []memory.Turn) {
	f.mu.Lock()
	f.seeded = append(f.seeded, t)
	f.mu.Unlock()
}
func (f *fakeContext) Reset() { f.mu.Lock(); f.resets++; f.mu.Unlock() }

// A reopened conversation reaches the model's context, and a new chat
// clears it again.
func TestContextSeededFromStoredConversation(t *testing.T) {
	tests := []struct {
		name string
		msgs []history.Message
		want []memory.Turn
	}{
		{"pairs", []history.Message{
			{Role: history.RoleUser, Text: "q1"}, {Role: history.RoleAssistant, Text: "a1"},
			{Role: history.RoleUser, Text: "q2"}, {Role: history.RoleAssistant, Text: "a2"},
		}, []memory.Turn{{User: "q1", Assistant: "a1"}, {User: "q2", Assistant: "a2"}}},
		{"unanswered turn and tool messages", []history.Message{
			{Role: history.RoleUser, Text: "q1"}, {Role: history.RoleTool, Text: "t"},
			{Role: history.RoleUser, Text: "q2"}, {Role: history.RoleAssistant, Text: "a2"},
		}, []memory.Turn{{User: "q1"}, {User: "q2", Assistant: "a2"}}},
		{"leading assistant ignored", []history.Message{
			{Role: history.RoleAssistant, Text: "hello"}, {Role: history.RoleUser, Text: "q"},
		}, []memory.Turn{{User: "q"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{convs: []*history.Conversation{{ID: "c1", Title: "t", Messages: tt.msgs}}}
			fc := &fakeContext{}
			s, _, ch := start(t, Config{History: st, Context: fc})
			settle(t, ch, func(v View) bool { return len(v.Messages) > 0 })
			fc.mu.Lock()
			got := fc.seeded
			fc.mu.Unlock()
			if len(got) != 1 || !reflect.DeepEqual(got[0], tt.want) {
				t.Fatalf("seeded %+v, want %+v", got, tt.want)
			}
			if err := s.NewChat(context.Background()); err != nil {
				t.Fatal(err)
			}
			fc.mu.Lock()
			defer fc.mu.Unlock()
			if fc.resets != 1 {
				t.Errorf("resets = %d", fc.resets)
			}
		})
	}
}
