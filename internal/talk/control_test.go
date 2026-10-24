package talk

import (
	"context"
	"encoding/json"
	"image"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio"
	"github.com/xlsft/companion/internal/audio/playback"
	"github.com/xlsft/companion/internal/chat"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/tts"
	"github.com/xlsft/companion/pkg/contracts"
)

type fakeScene struct {
	mu      sync.Mutex
	clears  int
	endings []contracts.SessionRef
}

func (f *fakeScene) Clear(context.Context) { f.mu.Lock(); f.clears++; f.mu.Unlock() }
func (f *fakeScene) EndSession(_ context.Context, r contracts.SessionRef) {
	f.mu.Lock()
	f.endings = append(f.endings, r)
	f.mu.Unlock()
}

type fakeSession struct {
	posted []session.Event
	snaps  chan session.Snapshot
}

func (f *fakeSession) Post(ev session.Event) error { f.posted = append(f.posted, ev); return nil }
func (f *fakeSession) Subscribe() (<-chan session.Snapshot, func()) {
	return f.snaps, func() {}
}

func TestControlPostClearsOnEscape(t *testing.T) {
	tests := []struct {
		name   string
		ev     session.Event
		clears int
	}{
		{"escape", session.Cancel{}, 1},
		{"cancel of a specific generation", session.Cancel{Ref: ref}, 0},
		{"new request", session.SubmitText{Text: "x"}, 0},
		{"hotkey", session.HotkeyDown{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, ss, g := &fakeScene{}, &fakeSession{}, &fakeGuide{}
			c := &Control{Session: ss, Scene: sc, Guide: g}
			if err := c.Post(tt.ev); err != nil {
				t.Fatal(err)
			}
			if len(ss.posted) != 1 || ss.posted[0] != tt.ev {
				t.Fatalf("posted %v", ss.posted)
			}
			if sc.clears != tt.clears {
				t.Errorf("clears %d, want %d", sc.clears, tt.clears)
			}
			// Escape ends a walkthrough together with what is on screen.
			if g.stops != tt.clears {
				t.Errorf("walkthrough stops %d, want %d", g.stops, tt.clears)
			}
		})
	}
	// Without an overlay Escape still reaches the session.
	ss := &fakeSession{}
	if err := (&Control{Session: ss}).Post(session.Cancel{}); err != nil || len(ss.posted) != 1 {
		t.Fatalf("posted %v, err %v", ss.posted, err)
	}
}

func TestControlRunEndsSceneOfAbortedGenerations(t *testing.T) {
	tests := []struct {
		state session.State
		ends  bool
	}{
		{session.StateCancelled, true},
		{session.StateInterrupted, true},
		{session.StateFailed, true},
		{session.StateIdle, false}, // a finished answer keeps its hints
		{session.StateSpeaking, false},
		{session.StateAwaitingInput, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			sc := &fakeScene{}
			ss := &fakeSession{snaps: make(chan session.Snapshot, 1)}
			c := &Control{Session: ss, Scene: sc}
			done := make(chan struct{})
			go func() { c.Run(context.Background()); close(done) }()
			ss.snaps <- session.Snapshot{Ref: ref, State: tt.state}
			close(ss.snaps)
			<-done
			if got := len(sc.endings) == 1 && sc.endings[0] == ref; got != tt.ends {
				t.Errorf("endings %v, want ended=%v", sc.endings, tt.ends)
			}
		})
	}
}

// The tests below run the real pipeline (session manager, orchestrator,
// TTS speaker over the jitter-buffered player, overlay engine, chat) with
// fakes at the edges and measure how fast an interruption takes effect.

// talkingProvider answers the first request by pointing at the screen and
// then keeps the stream open until it is cancelled, so the answer is
// mid-stream and mid-speech when the test interrupts it. Any other request
// says one sentence and also stays open.
type talkingProvider struct {
	mu        sync.Mutex
	cancelled []time.Time // when each stream saw its ctx end
}

func (p *talkingProvider) Capabilities() llm.Capabilities { return llm.Capabilities{} }

func (p *talkingProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	var evs []llm.Event
	last := req.Messages[len(req.Messages)-1]
	switch {
	case last.Role == llm.RoleTool:
		evs = []llm.Event{llm.TextDelta{Text: " Click it."}}
	case len(req.Tools) > 0 && last.Content[0].Text == "where is it?":
		evs = []llm.Event{
			llm.TextDelta{Text: "It is here."},
			llm.ToolCallCompleted{ID: "c1", Name: overlay.ToolPointAt, Arguments: json.RawMessage(`{"x":0.5,"y":0.5}`)},
			llm.Completed{},
		}
		return &sliceStream{events: evs}, nil
	default:
		evs = []llm.Event{llm.TextDelta{Text: "Okay."}}
	}
	return &openStream{ctx: ctx, events: evs, p: p}, nil
}

// openStream yields its events, then blocks until ctx ends.
type openStream struct {
	ctx    context.Context
	events []llm.Event
	p      *talkingProvider
}

func (s *openStream) Recv() (llm.Event, error) {
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	}
	<-s.ctx.Done()
	s.p.mu.Lock()
	s.p.cancelled = append(s.p.cancelled, time.Now())
	s.p.mu.Unlock()
	return nil, s.ctx.Err()
}
func (s *openStream) Close() error { return nil }

func (p *talkingProvider) firstCancel() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cancelled) == 0 {
		return time.Time{}, false
	}
	return p.cancelled[0], true
}

type shotCapturer struct{}

func (shotCapturer) Capture(context.Context, contracts.CaptureRequest) ([]*contracts.Capture, error) {
	return []*contracts.Capture{screenCapture()}, nil
}

// syncHost is an overlay host safe for the engine's background goroutines.
type syncHost struct {
	mu   sync.Mutex
	open int
}

type syncSurface struct {
	h      *syncHost
	d      contracts.DisplayID
	closed bool
}

func (h *syncHost) Open(_ context.Context, d contracts.DisplayID) (contracts.OverlaySurface, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.open++
	return &syncSurface{h: h, d: d}, nil
}

func (h *syncHost) surfaces() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open
}

func (s *syncSurface) Display() contracts.DisplayID               { return s.d }
func (s *syncSurface) Present(context.Context, *image.RGBA) error { return nil }
func (s *syncSurface) SetInteractive(contracts.Rect) error        { return nil }
func (s *syncSurface) Close() error {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.h.open--
	}
	return nil
}

type pipeline struct {
	m     *session.Manager
	ctl   *Control
	chat  *chat.Service
	prov  *talkingProvider
	sink  *playback.FakeSink
	host  *syncHost
	mic   *audio.FakeSource
	snaps <-chan session.Snapshot
}

func newPipeline(t *testing.T) *pipeline {
	t.Helper()
	p := &pipeline{
		prov: &talkingProvider{},
		sink: &playback.FakeSink{Realtime: true},
		host: &syncHost{},
		mic:  &audio.FakeSource{Signal: audio.SignalSine, Amplitude: 0.5, Realtime: true},
	}
	display := contracts.Display{ID: "d1", Bounds: contracts.RectXYWH(0, 0, 1920, 1080), Native: contracts.PixelSize{W: 3840, H: 2160}, Scale: 2}
	engine := overlay.NewEngine(p.host, func(context.Context) (contracts.Topology, error) {
		return contracts.Topology{Version: 1, Displays: []contracts.Display{display}}, nil
	})
	engine.SetMotion(false)
	orch := &Orchestrator{
		Provider: func(context.Context) (llm.Provider, error) { return p.prov, nil },
		Overlay:  engine,
		// 400 chunks of 50 ms, 5 ms apart: the reply would speak for 20 s.
		Speaker:  &TTSSpeaker{Provider: &tts.Fake{Chunks: 400, ChunkBytes: 2400, Delay: 5 * time.Millisecond}, Player: playback.New(p.sink), Voice: "v"},
		Capturer: shotCapturer{},
		Recorder: &MicRecorder{Mic: audio.NewCapturer(p.mic), STT: ClientSTT(inProcessSTT(t))},
	}
	p.m = session.New(orch, session.Config{Settings: session.Settings{
		VoiceMode: session.ModeToggle, Capture: session.CaptureAtEnd, SpeakReplies: true,
	}})
	p.ctl = &Control{Session: p.m, Scene: engine}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.ctl.Run(ctx) }()
	p.chat = chat.New(chat.Config{Talk: p.ctl})
	if err := p.chat.Start(ctx); err != nil {
		t.Fatal(err)
	}
	snaps, unsub := p.m.Subscribe()
	p.snaps = snaps
	t.Cleanup(func() {
		unsub()
		p.chat.Close()
		cancel()
		wg.Wait()
		_ = p.m.Close()
	})
	return p
}

// await reads snapshots until one has state want and returns it.
func (p *pipeline) await(t *testing.T, want session.State) session.Snapshot {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-p.snaps:
			if s.State == session.StateFailed {
				t.Fatalf("session failed: %s %v", s.Reason, s.Err)
			}
			if s.State == want {
				return s
			}
		case <-timeout:
			t.Fatalf("no %s within 5s; now %s", want, p.m.Current().State)
		}
	}
}

// within polls cond and returns how long it took to become true.
func within(t *testing.T, what string, t0 time.Time, cond func() bool) time.Duration {
	t.Helper()
	deadline := t0.Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not after 2s", what)
		}
		time.Sleep(500 * time.Microsecond)
	}
	return time.Since(t0)
}

// maxStop is the interruption budget (FR-VOICE-005/006): speech silent and
// hints gone within 100 ms. The race detector slows everything several
// times over, so -race runs get a wider budget; the plain run (make test)
// holds the real one.
const maxStop = 100 * time.Millisecond * raceSlowdown

func TestInterruptStopsTheAnswer(t *testing.T) {
	tests := []struct {
		name string
		act  func(p *pipeline) error
		// next is the state the replacing request reaches; empty for a
		// cancel, which ends in Idle.
		next session.State
	}{
		{"escape", func(p *pipeline) error { return p.ctl.Post(session.Cancel{}) }, ""},
		{"chat stop", func(p *pipeline) error { return p.chat.Stop(context.Background()) }, ""},
		{"chat send barges in", func(p *pipeline) error { return p.chat.Send(context.Background(), "and now?") }, session.StateResponding},
		{"hotkey barges in", func(p *pipeline) error { return p.ctl.Post(session.HotkeyDown{}) }, session.StateListening},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPipeline(t)
			if err := p.ctl.Post(session.SubmitText{Text: "where is it?"}); err != nil {
				t.Fatal(err)
			}
			speaking := p.await(t, session.StateSpeaking)
			old := speaking.Ref
			within(t, "hint shown", time.Now(), func() bool { return p.host.surfaces() == 1 })
			out := p.sink.Last()

			t0 := time.Now()
			if err := tt.act(p); err != nil {
				t.Fatal(err)
			}
			silent := within(t, "speech stopped", t0, func() bool { closed, _ := out.Closed(); return closed })
			cleared := within(t, "overlay cleared", t0, func() bool { return p.host.surfaces() == 0 })
			streamEnd := within(t, "model stream cancelled", t0, func() bool { _, ok := p.prov.firstCancel(); return ok })
			t.Logf("audio silent after %v, overlay cleared after %v, model stream cancelled after %v", silent, cleared, streamEnd)
			if silent > maxStop || cleared > maxStop {
				t.Errorf("interruption too slow: audio %v, overlay %v (budget %v)", silent, cleared, maxStop)
			}

			// Nothing of the old generation after its terminal snapshot.
			terminal := false
			timeout := time.After(5 * time.Second)
			for done := false; !done; {
				select {
				case s := <-p.snaps:
					if s.Ref == old {
						if terminal && s.State != session.StateIdle {
							t.Fatalf("old generation published %s after it ended", s.State)
						}
						if s.State == session.StateCancelled || s.State == session.StateInterrupted {
							terminal = true
						}
						if s.State == session.StateIdle && terminal && tt.next == "" {
							done = true
						}
						continue
					}
					if !terminal {
						t.Fatalf("new generation %v before the old one ended", s.Ref)
					}
					if s.State == tt.next {
						done = true
					}
				case <-timeout:
					t.Fatalf("did not settle; state %s", p.m.Current().State)
				}
			}
			// A new request is a follow-up in the same session, not a
			// continuation of the interrupted generation.
			if tt.next != "" {
				if cur := p.m.Current().Ref; cur.ID != old.ID || cur.Generation <= old.Generation {
					t.Errorf("new generation %v after %v", cur, old)
				}
				_ = p.ctl.Post(session.Cancel{})
				p.await(t, session.StateIdle)
			}
			if p.sink.Active() != 0 {
				t.Errorf("%d audio outputs open after cancel", p.sink.Active())
			}
			// The interrupted reply stays as it was and is marked so.
			v, unsub := p.chat.Subscribe()
			view := <-v
			unsub()
			var reply *chat.Message
			for i := range view.Messages {
				if view.Messages[i].Role == chat.RoleAssistant {
					reply = &view.Messages[i]
					break
				}
			}
			if reply == nil || !reply.Interrupted || reply.Streaming {
				t.Errorf("interrupted reply %+v", reply)
			}
		})
	}
}

func TestEscapeWhileListeningReleasesMic(t *testing.T) {
	p := newPipeline(t)
	if err := p.ctl.Post(session.HotkeyDown{}); err != nil {
		t.Fatal(err)
	}
	p.await(t, session.StateListening)
	within(t, "mic open", time.Now(), func() bool { return p.mic.Active() == 1 })
	t0 := time.Now()
	if err := p.ctl.Post(session.Cancel{}); err != nil {
		t.Fatal(err)
	}
	released := within(t, "mic released", t0, func() bool { return p.mic.Active() == 0 })
	t.Logf("microphone released after %v", released)
	if released > maxStop {
		t.Errorf("mic released after %v (budget %v)", released, maxStop)
	}
	if s := p.await(t, session.StateIdle); s.Reason != "cancel" {
		t.Errorf("idle reason %q", s.Reason)
	}
}

func TestEscapeWhenIdleClearsLeftoverHints(t *testing.T) {
	topo := contracts.Topology{Version: 1, Displays: []contracts.Display{{ID: "d1", Bounds: contracts.RectXYWH(0, 0, 1920, 1080), Native: contracts.PixelSize{W: 3840, H: 2160}, Scale: 2}}}
	host := &syncHost{}
	engine := overlay.NewEngine(host, func(context.Context) (contracts.Topology, error) { return topo, nil })
	engine.SetMotion(false)
	engine.RegisterCapture(screenCapture().Meta)
	// A finished answer leaves its hint up for its lifetime.
	if err := engine.Apply(context.Background(), overlay.Command{
		Op: overlay.OpShow, ID: "h", Session: ref, Capture: "cap1", Kind: overlay.KindRing,
		Points: []contracts.Point{{X: 0.5, Y: 0.5}}, Radius: 0.05, Segment: -1,
	}); err != nil {
		t.Fatal(err)
	}
	m := session.New(nil, session.Config{})
	defer m.Close()
	if err := (&Control{Session: m, Scene: engine}).Post(session.Cancel{}); err != nil {
		t.Fatal(err)
	}
	if n := host.surfaces(); n != 0 {
		t.Fatalf("%d overlay surfaces after Escape", n)
	}
}
