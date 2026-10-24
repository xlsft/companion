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

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/platform/detect"
	"github.com/xlsft/companion/internal/providers"
	"github.com/xlsft/companion/internal/secrets"
	"github.com/xlsft/companion/internal/session"
	"github.com/xlsft/companion/internal/settings"
	"github.com/xlsft/companion/internal/store"
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
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.chat(w, r)
	case strings.HasSuffix(r.URL.Path, "/audio/speech"):
		f.speech(w)
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
	f.mu.Unlock()

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

// speech answers with 0.4 s of a quiet 440 Hz tone, 24 kHz mono PCM16.
func (f *fakeAPI) speech(w http.ResponseWriter) {
	f.mu.Lock()
	f.speeches++
	f.mu.Unlock()
	const rate, dur = 24000, 0.4
	buf := make([]byte, 2*int(rate*dur))
	for i := range len(buf) / 2 {
		v := int16(0.04 * math.MaxInt16 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(buf[2*i:], uint16(v))
	}
	w.Header().Set("Content-Type", "audio/pcm")
	_, _ = w.Write(buf)
}

func TestLiveTalk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "companion.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
	defer c.Stop(context.Background())

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
