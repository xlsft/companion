package talk

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/audio/playback"
	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/session"
	"github.com/xlsft/openclick/internal/tts"
)

func noLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				t.Errorf("goroutines leaked: %d before, %d after\n%s", before, runtime.NumGoroutine(), buf[:runtime.Stack(buf, true)])
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func TestTTSSpeakerPlaysSegment(t *testing.T) {
	noLeaks(t)
	prov := &tts.Fake{Chunks: 4, ChunkBytes: 4800}
	player := &playback.FakePlayer{}
	sp := &TTSSpeaker{Provider: prov, Player: player, Voice: "nova", Speed: func() float64 { return 1.25 }}
	var calls atomic.Int32
	if err := sp.Say(context.Background(), "Hello there.", func() { calls.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("started called %d times, want 1", n)
	}
	plays := player.Plays()
	if len(plays) != 1 || plays[0].Bytes != 4*4800 || plays[0].Err != nil {
		t.Fatalf("plays %+v", plays)
	}
	if plays[0].Format != (playback.Format{SampleRate: 24000, Channels: 1}) {
		t.Errorf("format %+v", plays[0].Format)
	}
	req := prov.Requests()[0]
	if req.Text != "Hello there." || req.VoiceID != "nova" || req.Speed != 1.25 || req.Format != tts.PCM24kMono {
		t.Errorf("request %+v", req)
	}
}

func TestTTSSpeakerSpeedIsClamped(t *testing.T) {
	tests := []struct{ in, want float64 }{
		{0, 1}, {0.2, 0.5}, {0.75, 0.75}, {3, 1.5},
	}
	for _, tt := range tests {
		sp := &TTSSpeaker{Voice: "v", Speed: func() float64 { return tt.in }}
		if got := sp.request("x").Speed; got != tt.want {
			t.Errorf("Speed %v -> %v, want %v", tt.in, got, tt.want)
		}
	}
	if got := (&TTSSpeaker{}).request("x").Speed; got != tts.DefaultSpeed {
		t.Errorf("nil Speed -> %v", got)
	}
}

// TestTTSSpeakerOnRealPlayer runs the whole path through the jitter buffer
// and a simulated device clock.
func TestTTSSpeakerOnRealPlayer(t *testing.T) {
	noLeaks(t)
	sink := &playback.FakeSink{Realtime: true}
	sp := &TTSSpeaker{Provider: &tts.Fake{Chunks: 3, ChunkBytes: 2400, Delay: 5 * time.Millisecond}, Player: playback.New(sink), Voice: "v"}
	var calls atomic.Int32
	t0 := time.Now()
	if err := sp.Say(context.Background(), "Hi.", func() { calls.Add(1) }); err != nil {
		t.Fatal(err)
	}
	// 150 ms of audio must actually have been played out, not skipped.
	if d := time.Since(t0); d < 150*time.Millisecond {
		t.Errorf("Say returned after %v, before its 150 ms of audio", d)
	}
	if calls.Load() != 1 || sink.Active() != 0 {
		t.Errorf("started %d times, %d outputs still open", calls.Load(), sink.Active())
	}
}

func TestTTSSpeakerCancelAbortsSynthesisAndPlayback(t *testing.T) {
	noLeaks(t)
	tests := []struct {
		name   string
		player func() (playback.Player, func() int)
	}{
		{"fake player", func() (playback.Player, func() int) {
			p := &playback.FakePlayer{Realtime: true}
			return p, p.Active
		}},
		{"real player", func() (playback.Player, func() int) {
			s := &playback.FakeSink{Realtime: true}
			return playback.New(s), s.Active
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 100 chunks 20 ms apart: synthesis would take two seconds.
			prov := &tts.Fake{Chunks: 100, ChunkBytes: 2400, Delay: 20 * time.Millisecond}
			player, active := tt.player()
			sp := &TTSSpeaker{Provider: prov, Player: player, Voice: "v"}
			ctx, cancel := context.WithCancel(context.Background())
			startedCh := make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- sp.Say(ctx, "A long answer.", func() { close(startedCh) }) }()
			select {
			case <-startedCh:
			case <-time.After(2 * time.Second):
				t.Fatal("never started")
			}
			t0 := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("Say = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Say did not return after cancel")
			}
			if d := time.Since(t0); d > 150*time.Millisecond {
				t.Errorf("cancel took %v", d)
			}
			if n := active(); n != 0 {
				t.Errorf("%d playbacks still active", n)
			}
		})
	}
}

func TestTTSSpeakerSynthesisErrorIsReturned(t *testing.T) {
	noLeaks(t)
	boom := errors.New("auth")
	player := &playback.FakePlayer{}
	sp := &TTSSpeaker{Provider: &tts.Fake{Err: boom}, Player: player, Voice: "v"}
	called := false
	if err := sp.Say(context.Background(), "x", func() { called = true }); !errors.Is(err, boom) {
		t.Fatalf("Say = %v", err)
	}
	if called || len(player.Plays()) != 0 {
		t.Error("played or signalled start after a synthesis error")
	}
	if err := (&TTSSpeaker{}).Say(context.Background(), "x", nil); err == nil {
		t.Error("unconfigured speaker succeeded")
	}
}

func TestTTSSpeakerPrefetch(t *testing.T) {
	noLeaks(t)
	ctx := context.Background()
	prov := &tts.Fake{Chunks: 2, ChunkBytes: 4800}
	player := &playback.FakePlayer{}
	sp := &TTSSpeaker{Provider: prov, Player: player, Voice: "v"}

	sp.Prefetch(ctx, "Second.")
	if err := sp.Say(ctx, "Second.", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(prov.Requests()); n != 1 {
		t.Errorf("%d synthesis requests for a prefetched segment, want 1", n)
	}

	// A prefetch issued before the Say of the segment ahead of it (the
	// pipeline races like this) survives that Say and is used afterwards.
	sp.Prefetch(ctx, "Third.")
	if err := sp.Say(ctx, "Other.", nil); err != nil {
		t.Fatal(err)
	}
	if err := sp.Say(ctx, "Third.", nil); err != nil {
		t.Fatal(err)
	}
	// The discarded prefetch may reach the provider before or after Say.
	var texts []string
	for _, r := range prov.Requests() {
		texts = append(texts, r.Text)
	}
	if j := strings.Join(texts, ","); j != "Second.,Third.,Other." && j != "Second.,Other.,Third." {
		t.Fatalf("requests %s", j)
	}
	if plays := player.Plays(); len(plays) != 3 || plays[2].Bytes != 2*4800 {
		t.Fatalf("plays %+v", plays)
	}

	// Prefetches that were skipped over are discarded, and their streams
	// closed, when a later segment is said.
	sp.Prefetch(ctx, "Skipped.")
	sp.Prefetch(ctx, "Fourth.")
	if err := sp.Say(ctx, "Fourth.", nil); err != nil {
		t.Fatal(err)
	}
	sp.mu.Lock()
	left := len(sp.pre)
	sp.mu.Unlock()
	if left != 0 {
		t.Errorf("%d prefetches left after the skipped one", left)
	}
}

// prefetchSpeaker records the order of prefetches and says, and holds each
// Say until the next segment had a chance to be queued.
type prefetchSpeaker struct {
	mu  sync.Mutex
	log []string
}

func (s *prefetchSpeaker) add(e string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, e)
}

func (s *prefetchSpeaker) Prefetch(_ context.Context, text string) { s.add("prefetch:" + text) }

func (s *prefetchSpeaker) Say(_ context.Context, text string, started func()) error {
	started()
	s.add("say:" + text)
	time.Sleep(20 * time.Millisecond)
	return nil
}

func TestSpeechPipePrefetchesNextSegment(t *testing.T) {
	p := &scriptedProvider{rounds: [][]llm.Event{{
		llm.TextDelta{Text: "Open the File menu. Then press Export. Pick a folder and save."},
		llm.Completed{},
	}}}
	sp := &prefetchSpeaker{}
	o := &Orchestrator{Provider: func(context.Context) (llm.Provider, error) { return p, nil }, Speaker: sp}
	ev := &events{}
	if _, err := o.Reason(context.Background(), ref, session.Request{Text: "export?"}, ev.emit); err != nil {
		t.Fatal(err)
	}
	// Prefetches race with the Say they overlap, but each segment after the
	// first is prefetched before it is said, and says keep answer order.
	segs := []string{"Open the File menu.", "Then press Export.", "Pick a folder and save."}
	at := map[string]int{}
	var says []string
	for i, e := range sp.log {
		at[e] = i
		if text, ok := strings.CutPrefix(e, "say:"); ok {
			says = append(says, text)
		}
	}
	if strings.Join(says, "|") != strings.Join(segs, "|") || len(sp.log) != 5 {
		t.Fatalf("log %q", sp.log)
	}
	for _, s := range segs[1:] {
		if i, ok := at["prefetch:"+s]; !ok || i > at["say:"+s] {
			t.Errorf("%q not prefetched before it was said: %q", s, sp.log)
		}
	}
	if k := strings.Join(ev.kinds(), ","); strings.Count(k, "speech_started") != 1 || !strings.HasSuffix(k, "speech_done") {
		t.Errorf("events %s", k)
	}
}
