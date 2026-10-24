//go:build live

// The live test runs a typed Talk request through the real core on this
// machine (display tracking, screen capture, overlay, audio output) with a
// local OpenAI-compatible stand-in for the model and the speech service,
// so the whole pipeline can be checked without an API key:
//
//	go test -tags=novulkan,live -run TestLiveTalk -v ./cmd/companion
//
// It needs a desktop session; it plays a short quiet tone.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/companion/internal/audio/playback"
	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/overlay"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
	"github.com/xlsft/companion/internal/talk"
	"github.com/xlsft/companion/internal/walkthrough"
	"github.com/xlsft/companion/pkg/contracts"
)

// fakeAPI is an OpenAI-compatible model that points at the screen centre
// on the first round and answers in text after the tool result, plus a
// speech endpoint that returns a tone.
type fakeAPI struct {
	mu        sync.Mutex
	chats     int
	sawImage  bool
	sawTool   bool
	speeches  int
	firstByte time.Duration

	// hold keeps the answer after the tool round streaming and the speech
	// playing for many seconds, so a test can interrupt both mid-way.
	hold bool
	// gone lists the held responses ("chat", "speech") whose client went
	// away before the end.
	gone []string

	// walk makes the model plan a walkthrough and mark its steps.
	walk bool
	// spoken are the texts sent to the speech endpoint, in order.
	spoken []string
	// marked lists the steps (1-based) the model re-marked.
	marked []int
	// confirm makes the model say a step is done when asked after the
	// screen changed; checks counts those requests.
	confirm bool
	checks  int
}

func (f *fakeAPI) abandoned(kind string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.gone {
		if g == kind {
			return true
		}
	}
	return false
}

func (f *fakeAPI) markGone(kind string) {
	f.mu.Lock()
	f.gone = append(f.gone, kind)
	f.mu.Unlock()
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.chat(w, r)
	case strings.HasSuffix(r.URL.Path, "/audio/speech"):
		f.speech(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) chat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	toolRound := false
	// The barge-in request gets a short plain answer, so the hints the
	// test watches can only come from the interrupted generation.
	followUp := len(req.Messages) > 0 && strings.Contains(string(req.Messages[len(req.Messages)-1].Content), bargeText)
	f.mu.Lock()
	f.chats++
	for _, m := range req.Messages {
		if m.Role == "tool" {
			toolRound = true
			f.sawTool = true
		}
		if strings.Contains(string(m.Content), "image_url") {
			f.sawImage = true
		}
	}
	walk := f.walk
	f.mu.Unlock()
	var last string
	if n := len(req.Messages); n > 0 {
		last = string(req.Messages[n-1].Content)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		time.Sleep(30 * time.Millisecond)
	}
	delta := func(d map[string]any) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": d}}}
	}
	if walk {
		f.walkChat(body, last, toolRound, send, delta)
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
		return
	}
	if followUp {
		send(delta(map[string]any{"content": "Хорошо."}))
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
		return
	}
	if toolRound && f.hold {
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				f.markGone("chat")
				return
			case <-time.After(200 * time.Millisecond):
			}
			send(delta(map[string]any{"content": fmt.Sprintf("часть %d ", i)}))
		}
	}
	if toolRound {
		for _, part := range []string{"Готово, ", "я показал её ", "на экране."} {
			send(delta(map[string]any{"content": part}))
		}
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	} else {
		send(delta(map[string]any{"content": "Кнопка в центре экрана. "}))
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1",
			"function": map[string]any{"name": "point_at", "arguments": `{"x":0.5,"y":0.5,"label":"Здесь"}`}}}}))
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}

// speech answers with 0.4 s of a quiet 440 Hz tone, 24 kHz mono PCM16;
// held, it streams 20 s of it in real time.
func (f *fakeAPI) speech(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input string `json:"input"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	f.spoken = append(f.spoken, in.Input)
	f.speeches++
	hold := f.hold
	f.mu.Unlock()
	dur := 0.4
	if hold {
		dur = 20
	}
	const rate = 24000
	buf := make([]byte, 2*int(rate*dur))
	for i := range len(buf) / 2 {
		v := int16(0.04 * math.MaxInt16 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(v))
	}
	w.Header().Set("Content-Type", "audio/pcm")
	if !hold {
		_, _ = w.Write(buf)
		return
	}
	fl := w.(http.Flusher)
	const chunk = 2 * rate / 20 // 50 ms
	for off := 0; off < len(buf); off += chunk {
		if _, err := w.Write(buf[off:min(off+chunk, len(buf))]); err != nil {
			f.markGone("speech")
			return
		}
		fl.Flush()
		select {
		case <-r.Context().Done():
			f.markGone("speech")
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// liveCore builds the real core against api and starts it.
func liveCore(t *testing.T, ctx context.Context, api *fakeAPI, wrap func(playback.Player) playback.Player) *core {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := settings.NewService(ctx, store.NewKV(db))
	if err != nil {
		t.Fatal(err)
	}
	report, err := detect.New().Detect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newCore(ctx, log, dir, db, svc, report, secrets.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("capture=%v overlay=%v", c.captureErr, c.overlayErr)
	if wrap != nil {
		c.player = wrap(c.player)
	}

	if _, err := c.providers.Create(ctx, providers.Input{
		Name: "local stand-in", Kind: llm.KindOpenAI, BaseURL: srv.URL + "/v1", Model: "stand-in",
		APIKey:    "test-key",
		Overrides: map[llm.Capability]bool{llm.CapVision: true, llm.CapTools: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.secrets.Put(ctx, ttsSecret(ttsOpenAI), []byte("test-key")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Update(ctx, func(s *settings.Settings) {
		s.TTS = settings.TTS{ProviderID: ttsOpenAI, Voice: "alloy", BaseURL: srv.URL + "/v1"}
	}); err != nil {
		t.Fatal(err)
	}
	c.rebuildSpeaker(ctx, svc.Get())

	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	return c
}

func TestLiveTalk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	api := &fakeAPI{}
	c := liveCore(t, ctx, api, nil)

	snaps, unsub := c.talk.Subscribe()
	defer unsub()
	start := time.Now()
	if err := c.talk.Post(session.SubmitText{Text: "где кнопка?"}); err != nil {
		t.Fatal(err)
	}
	var states []string
	var reply strings.Builder
	firstText := time.Duration(0)
	for {
		select {
		case s := <-snaps:
			if len(states) == 0 || states[len(states)-1] != string(s.State) {
				states = append(states, string(s.State))
				t.Logf("%7s %s %s", time.Since(start).Round(time.Millisecond), s.State, s.Reason)
			}
			if s.Text != "" && s.State != session.StateIdle {
				if firstText == 0 {
					firstText = time.Since(start)
				}
				reply.WriteString(s.Text)
			}
			if s.State == session.StateFailed {
				t.Fatalf("failed: %s %v", s.Reason, s.Err)
			}
			if s.State == session.StateIdle && len(states) > 1 {
				goto done
			}
		case <-ctx.Done():
			t.Fatalf("timed out; states %v", states)
		}
	}
done:
	api.mu.Lock()
	defer api.mu.Unlock()
	t.Logf("states %v; first text after %s; total %s; model rounds %d; speech requests %d",
		states, firstText.Round(time.Millisecond), time.Since(start).Round(time.Millisecond), api.chats, api.speeches)
	if !strings.Contains(reply.String(), "Готово") {
		t.Errorf("reply %q lacks the answer after the tool round", reply.String())
	}
	if c.captureErr == nil && !api.sawImage {
		t.Error("capture works but the model got no screenshot")
	}
	if c.overlayErr == nil && !api.sawTool {
		t.Error("overlay works but point_at was not executed")
	}
	if api.speeches == 0 {
		t.Error("reply was not spoken")
	}
}

// bargeText marks the request that interrupts the held answer.
const bargeText = "and now?"

// watchedPlayer remembers every playback so the test can see them end.
type watchedPlayer struct {
	playback.Player
	mu    sync.Mutex
	plays []playback.Playback
}

func (w *watchedPlayer) Play(ctx context.Context, src playback.ChunkSource, f playback.Format) (playback.Playback, error) {
	pb, err := w.Player.Play(ctx, src, f)
	if err == nil {
		w.mu.Lock()
		w.plays = append(w.plays, pb)
		w.mu.Unlock()
	}
	return pb, err
}

// playing counts the first n playbacks that have not ended.
func (w *watchedPlayer) playing(n int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	busy := 0
	for _, pb := range w.plays[:min(n, len(w.plays))] {
		select {
		case <-pb.Done():
		default:
			busy++
		}
	}
	return busy
}

func (w *watchedPlayer) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.plays)
}

// TestLiveTalkInterrupt interrupts an answer that is still streaming and
// speaking, with its hint on screen, and checks that the speaker and the
// overlay stop within the budget and the model and speech requests are
// abandoned (OC-65):
//
//	go test -tags=novulkan,live -run TestLiveTalkInterrupt -v ./cmd/companion
func TestLiveTalkInterrupt(t *testing.T) {
	tests := []struct {
		name string
		act  func(ctx context.Context, c *core) error
	}{
		{"escape", func(_ context.Context, c *core) error { return c.ctl.Post(session.Cancel{}) }},
		{"chat stop", func(ctx context.Context, c *core) error { return c.chat.Stop(ctx) }},
		{"new request", func(ctx context.Context, c *core) error { return c.chat.Send(ctx, bargeText) }},
	}
	const budget = 100 * time.Millisecond
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			api := &fakeAPI{hold: true}
			var player *watchedPlayer
			c := liveCore(t, ctx, api, func(p playback.Player) playback.Player {
				player = &watchedPlayer{Player: p}
				return player
			})
			snaps, unsub := c.talk.Subscribe()
			defer unsub()
			if err := c.ctl.Post(session.SubmitText{Text: "где кнопка?"}); err != nil {
				t.Fatal(err)
			}
			var old session.Snapshot
			for old.State != session.StateSpeaking {
				select {
				case old = <-snaps:
					if old.State == session.StateFailed {
						t.Fatalf("failed: %s %v", old.Reason, old.Err)
					}
				case <-ctx.Done():
					t.Fatal("never spoke")
				}
			}
			hints := c.overlay != nil && hintShown(c)
			t.Logf("speaking; hint on screen: %v", hints)

			// Let the tone be audible for a moment before interrupting it.
			time.Sleep(300 * time.Millisecond)
			n := player.count()
			t0 := time.Now()
			if err := tt.act(ctx, c); err != nil {
				t.Fatal(err)
			}
			wait := func(what string, cond func() bool) time.Duration {
				for !cond() {
					if time.Since(t0) > 3*time.Second {
						t.Fatalf("%s: not after 3s", what)
					}
					time.Sleep(time.Millisecond)
				}
				return time.Since(t0)
			}
			silent := wait("speaker stopped", func() bool { return player.playing(n) == 0 })
			var cleared time.Duration
			if hints {
				cleared = wait("overlay cleared", func() bool { return c.overlay.Shown() == 0 })
			}
			chatGone := wait("model stream abandoned", func() bool { return api.abandoned("chat") })
			speechGone := wait("speech stream abandoned", func() bool { return api.abandoned("speech") })
			t.Logf("speaker silent after %v, overlay cleared after %v, model stream gone after %v, speech stream gone after %v",
				silent, cleared, chatGone, speechGone)
			if silent > budget || cleared > budget {
				t.Errorf("interruption too slow: speaker %v, overlay %v (budget %v)", silent, cleared, budget)
			}

			// Nothing of the interrupted generation after its end.
			ended := false
			for settled := false; !settled; {
				select {
				case s := <-snaps:
					switch {
					case s.Ref == old.Ref && (s.State == session.StateCancelled || s.State == session.StateInterrupted):
						ended = true
					case s.Ref == old.Ref && s.State == session.StateIdle && ended:
						settled = true
					case s.Ref == old.Ref && ended:
						t.Fatalf("interrupted generation published %s", s.State)
					case s.Ref != old.Ref:
						if !ended {
							t.Fatalf("new generation before the old one ended")
						}
						settled = true
					}
				case <-ctx.Done():
					t.Fatal("did not settle")
				}
			}
			if hints && c.overlay.Shown() != 0 {
				t.Error("hint of the interrupted generation came back")
			}
			// The barge-in request is answered and spoken; stop it too.
			_ = c.ctl.Post(session.Cancel{})
			for s := c.talk.Current(); s.State != session.StateIdle; s = c.talk.Current() {
				if ctx.Err() != nil {
					t.Fatal("not idle after the final cancel")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

// hintShown waits briefly for the point_at hint to be on screen.
func hintShown(c *core) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.overlay.Shown() > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// walkSteps is the walkthrough the fake model plans, and walkMarks where
// it marks each step: the first on the plan's own screenshot, the others
// when they are shown again on a new one.
var (
	walkSteps = []string{"Шаг один: выделите фрейм.", "Шаг два: откройте экспорт.", "Шаг три: нажмите «Экспорт»."}
	walkMarks = [][2]float64{{0.3, 0.4}, {0.6, 0.5}, {0.7, 0.6}}
)

// walkChat answers the request with start_walkthrough, "what's next"
// with walkthrough_control, a request to mark step n on a new screenshot
// with an annotate call, and a check after the screen changed with
// walkthrough_control once the test has made the change.
func (f *fakeAPI) walkChat(req []byte, last string, toolRound bool, send func(any), delta func(map[string]any) map[string]any) {
	call := func(id, name string, args any) {
		b, _ := json.Marshal(args)
		send(delta(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id,
			"function": map[string]any{"name": name, "arguments": string(b)}}}}))
	}
	switch {
	case strings.Contains(string(req), "has just changed"):
		f.mu.Lock()
		f.checks++
		confirm := f.confirm
		f.mu.Unlock()
		if confirm && !toolRound {
			call("check", "walkthrough_control", map[string]any{"action": "next", "reason": "сделано"})
		}
		return
	case strings.Contains(last, "что дальше"):
		send(delta(map[string]any{"content": "Хорошо. "}))
		call("ctl", "walkthrough_control", map[string]any{"action": "next"})
		return
	}
	for n := range walkSteps {
		if !strings.Contains(string(req), "Mark where the user has to act") ||
			!strings.Contains(string(req), fmt.Sprintf("step %d of %d", n+1, len(walkSteps))) {
			continue
		}
		if toolRound {
			return // marked; nothing more to say
		}
		f.mu.Lock()
		f.marked = append(f.marked, n+1)
		f.mu.Unlock()
		m := walkMarks[n]
		call(fmt.Sprintf("mark_%d", n+1), "annotate", map[string]any{"shapes": []any{
			map[string]any{"kind": "brackets", "points": [][]float64{{m[0] - 0.05, m[1] - 0.03}, {m[0] + 0.05, m[1] + 0.03}}},
		}})
		return
	}
	var steps []any
	for i, say := range walkSteps {
		st := map[string]any{"title": fmt.Sprintf("Шаг %d", i+1), "say": say}
		if i == 0 {
			st["shapes"] = []any{map[string]any{"kind": "ring", "points": [][]float64{{walkMarks[0][0], walkMarks[0][1]}}}}
		}
		steps = append(steps, st)
	}
	send(delta(map[string]any{"content": "Давайте по шагам. "}))
	call("walk_1", "start_walkthrough", map[string]any{"goal": "Экспорт в PNG", "steps": steps})
}

func (f *fakeAPI) spokenCount(text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.spoken {
		if s == text {
			n++
		}
	}
	return n
}

// drawLog records what reaches the overlay, so the test can tell which
// step drew which hint.
type drawLog struct {
	talk.Overlay
	mu   sync.Mutex
	cmds []overlay.Command
}

func (d *drawLog) Apply(ctx context.Context, c overlay.Command) error {
	d.mu.Lock()
	d.cmds = append(d.cmds, c)
	d.mu.Unlock()
	return d.Overlay.Apply(ctx, c)
}

// lastShow is the centre of the newest hint drawn for a walkthrough.
func (d *drawLog) lastShow() (contracts.Point, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.cmds) - 1; i >= 0; i-- {
		c := d.cmds[i]
		if c.Op != overlay.OpShow || !strings.HasPrefix(string(c.Session.ID), "walkthrough-") {
			continue
		}
		if len(c.Points) == 2 {
			return contracts.Point{X: (c.Points[0].X + c.Points[1].X) / 2, Y: (c.Points[0].Y + c.Points[1].Y) / 2}, true
		}
		return c.Points[0], true
	}
	return contracts.Point{}, false
}

// paintedScreen is the screen as the step watcher samples it: the real
// capture, with its middle painted over once lit, as if the user's click
// had opened a panel there.
type paintedScreen struct {
	contracts.ScreenCapturer
	mu  sync.Mutex
	lit bool
	n   int
}

func (p *paintedScreen) light(on bool) { p.mu.Lock(); p.lit = on; p.mu.Unlock() }

func (p *paintedScreen) samples() int { p.mu.Lock(); defer p.mu.Unlock(); return p.n }

func (p *paintedScreen) Capture(ctx context.Context, req contracts.CaptureRequest) ([]*contracts.Capture, error) {
	caps, err := p.ScreenCapturer.Capture(ctx, req)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	if err != nil || !p.lit {
		return caps, err
	}
	for _, c := range caps {
		b := c.Image.Bounds()
		for y := b.Min.Y + b.Dy()/5; y < b.Max.Y-b.Dy()/5; y++ {
			for x := b.Min.X + b.Dx()/5; x < b.Max.X-b.Dx()/5; x++ {
				i := c.Image.PixOffset(x, y)
				copy(c.Image.Pix[i:i+4], []uint8{0x20, 0x60, 0xE0, 0xFF})
			}
		}
	}
	return caps, nil
}

// TestLiveWalkthrough has the model start a three-step walkthrough and
// drives it by voice through the model, from the card, by doing a step on
// the (painted) screen and with Escape, checking the hint and the speech
// of every step and the stored progress (OC-68):
//
//	go test -tags=novulkan,live -run TestLiveWalkthrough -v ./cmd/companion
func TestLiveWalkthrough(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	api := &fakeAPI{walk: true}
	c := liveCore(t, ctx, api, nil)
	var draws *drawLog
	if c.orch.Overlay != nil {
		draws = &drawLog{Overlay: c.orch.Overlay}
		c.orch.Overlay = draws
	}
	// The watcher samples a screen the test can change, faster than in
	// the app so the run stays short.
	var screen *paintedScreen
	if c.capturer != nil {
		screen = &paintedScreen{ScreenCapturer: c.capturer}
		presenter := &talk.Presenter{Orch: c.orch}
		if c.overlay != nil {
			presenter.Scene = c.overlay
		}
		c.guide.Close()
		c.guide = walkthrough.New(walkthrough.Config{Presenter: presenter, Store: walkthrough.NewKVStore(store.NewKV(c.db)),
			Watch: &walkthrough.Watch{Capturer: screen, Interval: 500 * time.Millisecond}})
		c.orch.Guide, c.ctl.Guide = c.guide, c.guide
	}
	t0 := time.Now()
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			if ctx.Err() != nil {
				t.Fatalf("%s: timed out", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	idle := func() {
		t.Helper()
		waitFor("session idle", func() bool { return c.talk.Current().State == session.StateIdle })
	}
	// step waits until step n (0-based) is on the card, marked where the
	// model put it, and spoken for the times-th time.
	step := func(how string, n, times int) {
		t.Helper()
		waitFor(how+": card", func() bool { v := c.guide.View(); return v.Active() && v.Index == n && !v.Preparing })
		waitFor(how+": speech", func() bool { return api.spokenCount(walkSteps[n]) >= times })
		hint := "no overlay"
		if draws != nil {
			want := walkMarks[n]
			waitFor(how+": hint", func() bool {
				p, ok := draws.lastShow()
				return ok && math.Abs(p.X-want[0]) < 1e-6 && math.Abs(p.Y-want[1]) < 1e-6
			})
			waitFor(how+": hint on screen", func() bool { return c.overlay.Shown() > 0 })
			hint = fmt.Sprintf("hint at %.2f,%.2f, %d on screen", want[0], want[1], c.overlay.Shown())
		}
		v := c.guide.View()
		t.Logf("%7s %-16s step %d/%d %q, %s, spoken", time.Since(t0).Round(time.Millisecond), how, v.Index+1, v.Total, v.Title, hint)
	}

	if err := c.ctl.Post(session.SubmitText{Text: "как экспортировать фрейм в PNG?"}); err != nil {
		t.Fatal(err)
	}
	step("planned", 0, 1)
	idle()
	if v := c.guide.View(); v.Goal != "Экспорт в PNG" || v.Total != 3 {
		t.Fatalf("view %+v", v)
	}

	// By voice: the model hears "what's next" and moves on.
	if err := c.ctl.Post(session.SubmitText{Text: "Готово, что дальше?"}); err != nil {
		t.Fatal(err)
	}
	step("voice next", 1, 1)
	idle()
	// From the card.
	if err := c.guide.Do(ctx, walkthrough.CmdNext); err != nil {
		t.Fatal(err)
	}
	step("card next", 2, 1)
	stored, ok, err := walkthrough.NewKVStore(store.NewKV(c.db)).Load(ctx)
	if err != nil || !ok || stored.Index != 2 || stored.Status != walkthrough.StatusRunning {
		t.Fatalf("stored progress %+v ok %v err %v", stored, ok, err)
	}
	if err := c.guide.Do(ctx, walkthrough.CmdPrev); err != nil {
		t.Fatal(err)
	}
	step("card previous", 1, 2)

	// Auto-advance: nothing happens while the screen stays as it is (the
	// hint's own glint included); once it changes and settles the model
	// is asked and confirms.
	at := 1
	if screen != nil {
		waitFor("watching", func() bool { return c.guide.View().Watching })
		n0 := screen.samples()
		waitFor("quiet samples", func() bool { return screen.samples() >= n0+4 })
		api.mu.Lock()
		quiet := api.checks
		api.confirm = true
		api.mu.Unlock()
		t.Logf("%7s watching, %d checks asked while the screen was quiet", time.Since(t0).Round(time.Millisecond), quiet)
		screen.light(true)
		lit := time.Now()
		step("auto-advance", 2, 2)
		t.Logf("advanced %v after the change", time.Since(lit).Round(time.Millisecond))
		screen.light(false)
		at = 2
	} else {
		t.Log("no screen capture: auto-advance not checked")
	}

	// A restart finds the walkthrough where it was, paused.
	again := walkthrough.New(walkthrough.Config{Presenter: &talk.Presenter{Orch: c.orch}, Store: walkthrough.NewKVStore(store.NewKV(c.db))})
	if err := again.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if v := again.View(); v.Status != walkthrough.StatusPaused || v.Index != at {
		t.Errorf("restored %+v, want paused at step %d", v, at+1)
	}
	again.Close()

	// Escape stops it and takes its hints off the screen.
	if err := c.ctl.Post(session.Cancel{}); err != nil {
		t.Fatal(err)
	}
	waitFor("stopped", func() bool { return !c.guide.View().Active() })
	if c.overlay != nil {
		waitFor("hints cleared", func() bool { return c.overlay.Shown() == 0 })
	}
	if _, ok, _ := walkthrough.NewKVStore(store.NewKV(c.db)).Load(ctx); ok {
		t.Error("stopped walkthrough still stored")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	t.Logf("model rounds %d, steps re-marked on new screenshots %v, step checks %d, speech requests %d", api.chats, api.marked, api.checks, api.speeches)
	if c.captureErr == nil && c.overlayErr == nil && fmt.Sprint(api.marked) != "[2 3 2 3]" {
		t.Errorf("re-marked steps %v, want [2 3 2 3]", api.marked)
	}
}
