package llmtest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/llm/anthropic"
	"github.com/xlsft/openclick/internal/llm/openai"
)

// Canaries that must never appear in an error string.
const (
	apiKey         = "sk-test-CANARY-KEY-0123456789"
	promptCanary   = "PROMPT-CANARY-what-is-on-my-screen"
	responseCanary = "RESPONSE-CANARY-secret-answer"
)

var leakyMessage = "invalid key " + apiKey + " for prompt " + promptCanary + " answer " + responseCanary

type adapter struct {
	dialect Dialect
	build   func(cfg llm.ProviderConfig, key string, hc *http.Client) (llm.Provider, error)
}

var adapters = []adapter{
	{OpenAI, func(c llm.ProviderConfig, k string, hc *http.Client) (llm.Provider, error) {
		c.Kind = llm.KindOpenAI
		return openai.New(c, k, hc)
	}},
	{Anthropic, func(c llm.ProviderConfig, k string, hc *http.Client) (llm.Provider, error) {
		c.Kind = llm.KindAnthropic
		return anthropic.New(c, k, hc)
	}},
}

func newProvider(t *testing.T, a adapter, caps map[llm.Capability]bool) (*Server, llm.Provider) {
	t.Helper()
	srv := NewServer(t, a.dialect)
	p, err := a.build(llm.ProviderConfig{
		Name:         "fake",
		BaseURL:      srv.BaseURL(),
		Model:        "fake-model",
		Timeout:      5 * time.Second,
		Capabilities: caps,
	}, apiKey, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return srv, p
}

func baseRequest() llm.ChatRequest {
	return llm.ChatRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: []llm.Part{llm.Text("You are a desktop assistant.")}},
		{Role: llm.RoleUser, Content: []llm.Part{llm.Text(promptCanary)}},
	}}
}

// collect runs one request; a Stream error becomes a terminal Failed so
// pre-stream and in-stream failures compare uniformly.
func collect(ctx context.Context, t *testing.T, p llm.Provider, req llm.ChatRequest) []llm.Event {
	t.Helper()
	s, err := p.Stream(ctx, req)
	if err != nil {
		return []llm.Event{llm.Failed{Err: err}}
	}
	defer s.Close()
	var out []llm.Event
	for {
		ev, err := s.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Recv error %v; errors must arrive as Failed", err)
		}
		out = append(out, ev)
		if len(out) > 10000 {
			t.Fatal("runaway stream")
		}
	}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-image-bytes")

type scenario struct {
	name    string
	req     func() llm.ChatRequest
	caps    map[llm.Capability]bool
	resp    Response
	want    []llm.Event // events before the terminal Failed, or including Completed
	wantErr error       // terminal Failed error; nil means the stream completes
	check   func(t *testing.T, d Dialect, srv *Server)
}

var usage = llm.Usage{InputTokens: 12, OutputTokens: 5, CacheReadTokens: 4}

var toolSteps = []Step{
	Text("Checking."),
	ToolStart("call_1", "point"),
	ToolArgs(`{"x"`), ToolArgs(`: 0.`), ToolArgs(`25, "y"`), ToolArgs(`: 0.5, "label": "Sa`), ToolArgs(`ve"}`),
	ToolEnd(),
	ToolStart("call_2", "say"),
	ToolArgs(`{"text":"hi"}`),
	ToolEnd(),
	Finish(llm.StopToolUse, usage),
}

var toolEvents = []llm.Event{
	llm.TextDelta{Text: "Checking."},
	llm.ToolCallStarted{Index: 0, ID: "call_1", Name: "point"},
	llm.ToolCallDelta{Index: 0, ArgumentsDelta: `{"x"`},
	llm.ToolCallDelta{Index: 0, ArgumentsDelta: `: 0.`},
	llm.ToolCallDelta{Index: 0, ArgumentsDelta: `25, "y"`},
	llm.ToolCallDelta{Index: 0, ArgumentsDelta: `: 0.5, "label": "Sa`},
	llm.ToolCallDelta{Index: 0, ArgumentsDelta: `ve"}`},
	llm.ToolCallCompleted{Index: 0, ID: "call_1", Name: "point", Arguments: json.RawMessage(`{"x": 0.25, "y": 0.5, "label": "Save"}`)},
	llm.ToolCallStarted{Index: 1, ID: "call_2", Name: "say"},
	llm.ToolCallDelta{Index: 1, ArgumentsDelta: `{"text":"hi"}`},
	llm.ToolCallCompleted{Index: 1, ID: "call_2", Name: "say", Arguments: json.RawMessage(`{"text":"hi"}`)},
	usage,
	llm.Completed{StopReason: llm.StopToolUse},
}

func toolRequest() llm.ChatRequest {
	req := baseRequest()
	req.Tools = []llm.Tool{
		{Name: "point", Description: "Point at a screen location", Schema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}}}`)},
		{Name: "say", Schema: json.RawMessage(`{"type":"object"}`)},
	}
	return req
}

func scenarios() []scenario {
	sc := []scenario{
		{
			name: "text stream",
			resp: Response{Steps: []Step{Text("Hel"), Text("lo, "), Text("world"), Finish(llm.StopEnd, usage)}},
			want: []llm.Event{llm.TextDelta{Text: "Hel"}, llm.TextDelta{Text: "lo, "}, llm.TextDelta{Text: "world"}, usage, llm.Completed{StopReason: llm.StopEnd}},
			check: func(t *testing.T, d Dialect, srv *Server) {
				rec := srv.Requests()[0]
				if rec.Body["stream"] != true || rec.Body["model"] != "fake-model" {
					t.Errorf("not a streaming request for the model: %v", rec.Body)
				}
				if d == OpenAI {
					opts, _ := rec.Body["stream_options"].(map[string]any)
					if opts["include_usage"] != true {
						t.Error("include_usage not requested")
					}
				} else {
					sys, _ := rec.Body["system"].([]any)
					if len(sys) != 1 {
						t.Errorf("system must be top-level: %v", rec.Body["system"])
					}
				}
			},
		},
		{
			name: "thinking then text",
			resp: Response{Steps: []Step{Think("Let me "), Think("see."), Text("Done."), Finish(llm.StopEnd, usage)}},
			want: []llm.Event{llm.ThinkingDelta{Text: "Let me "}, llm.ThinkingDelta{Text: "see."}, llm.TextDelta{Text: "Done."}, usage, llm.Completed{StopReason: llm.StopEnd}},
		},
		{
			name: "max tokens",
			resp: Response{Steps: []Step{Text("trunc"), Finish(llm.StopMaxTokens, usage)}},
			want: []llm.Event{llm.TextDelta{Text: "trunc"}, usage, llm.Completed{StopReason: llm.StopMaxTokens}},
		},
		{
			name: "tool call split across deltas",
			req:  toolRequest,
			resp: Response{Steps: toolSteps},
			want: toolEvents,
			check: func(t *testing.T, d Dialect, srv *Server) {
				tools, _ := srv.Requests()[0].Body["tools"].([]any)
				if len(tools) != 2 {
					t.Fatalf("tools not sent: %v", tools)
				}
				first := tools[0].(map[string]any)
				var schema any
				if d == OpenAI {
					if first["type"] != "function" {
						t.Errorf("not a function tool: %v", first)
					}
					schema = first["function"].(map[string]any)["parameters"]
				} else {
					schema = first["input_schema"]
				}
				if schema.(map[string]any)["type"] != "object" {
					t.Errorf("schema not sent: %v", first)
				}
			},
		},
		{
			name: "vision request",
			req: func() llm.ChatRequest {
				req := baseRequest()
				req.Attachments = []llm.Image{{MediaType: llm.MediaPNG, Data: pngBytes}}
				return req
			},
			resp: Response{Steps: []Step{Text("A save button."), Finish(llm.StopEnd, usage)}},
			want: []llm.Event{llm.TextDelta{Text: "A save button."}, usage, llm.Completed{StopReason: llm.StopEnd}},
			check: func(t *testing.T, d Dialect, srv *Server) {
				msgs := srv.Requests()[0].Body["messages"].([]any)
				last := msgs[len(msgs)-1].(map[string]any)
				content, _ := last["content"].([]any)
				if last["role"] != "user" || len(content) != 2 {
					t.Fatalf("image not in last user message: %v", last)
				}
				img := content[1].(map[string]any)
				b64 := base64.StdEncoding.EncodeToString(pngBytes)
				if d == OpenAI {
					url, _ := img["image_url"].(map[string]any)["url"].(string)
					if img["type"] != "image_url" || url != "data:image/png;base64,"+b64 {
						t.Errorf("bad image part: %v", img)
					}
				} else {
					src, _ := img["source"].(map[string]any)
					if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != b64 {
						t.Errorf("bad image block: %v", img)
					}
				}
			},
		},
		{
			name: "vision unsupported is not degraded",
			req: func() llm.ChatRequest {
				req := baseRequest()
				req.Attachments = []llm.Image{{MediaType: llm.MediaPNG, Data: pngBytes}}
				return req
			},
			caps:    map[llm.Capability]bool{llm.CapVision: false},
			resp:    Response{Steps: []Step{Text("x"), Finish(llm.StopEnd, usage)}},
			wantErr: &llm.ErrUnsupportedCapability{Cap: llm.CapVision},
			check: func(t *testing.T, d Dialect, srv *Server) {
				if n := len(srv.Requests()); n != 0 {
					t.Errorf("%d requests sent despite missing capability", n)
				}
			},
		},
		{
			name:    "malformed JSON event",
			resp:    Response{Steps: []Step{Text("partial"), Malformed(responseCanary)}},
			want:    []llm.Event{llm.TextDelta{Text: "partial"}},
			wantErr: llm.ErrMalformedStream,
		},
		{
			name:    "connection drop mid-stream",
			resp:    Response{Steps: []Step{Text("partial"), Drop()}},
			want:    []llm.Event{llm.TextDelta{Text: "partial"}},
			wantErr: llm.ErrNetwork,
		},
		{
			name:    "stream ends without end marker",
			resp:    Response{Steps: []Step{Text("partial")}},
			want:    []llm.Event{llm.TextDelta{Text: "partial"}},
			wantErr: llm.ErrMalformedStream,
		},
		{
			name:    "tool call truncated mid-arguments",
			req:     toolRequest,
			resp:    Response{Steps: []Step{ToolStart("call_1", "point"), ToolArgs(`{"x": 0.`), ToolEnd(), Finish(llm.StopMaxTokens, usage)}},
			want:    []llm.Event{llm.ToolCallStarted{Index: 0, ID: "call_1", Name: "point"}, llm.ToolCallDelta{Index: 0, ArgumentsDelta: `{"x": 0.`}},
			wantErr: llm.ErrMalformedStream,
		},
		{
			name:    "401",
			resp:    Response{Status: 401, ErrorMessage: leakyMessage},
			wantErr: llm.ErrAuth,
			check: func(t *testing.T, d Dialect, srv *Server) {
				h := srv.Requests()[0].Header
				if d == OpenAI {
					if h.Get("Authorization") != "Bearer "+apiKey {
						t.Error("missing bearer auth")
					}
				} else if h.Get("X-Api-Key") != apiKey || h.Get("Anthropic-Version") != "2023-06-01" || h.Get("Authorization") != "" {
					t.Errorf("bad anthropic headers: %v", h)
				}
			},
		},
		{
			name:    "429 with Retry-After",
			resp:    Response{Status: 429, Header: RetryAfterHeader(7 * time.Second), ErrorMessage: leakyMessage},
			wantErr: &llm.ErrRateLimited{RetryAfter: 7 * time.Second},
		},
		{
			name:    "context length",
			resp:    Response{Status: 400, ContextLength: true, ErrorMessage: leakyMessage},
			wantErr: llm.ErrContextLength,
		},
		{
			name:    "server error",
			resp:    Response{Status: 500, ErrorMessage: leakyMessage},
			wantErr: &llm.ErrProvider{Status: 500},
		},
	}
	// SSE split at arbitrary byte boundaries.
	for _, n := range []int{1, 2, 3, 7, 13, 64} {
		sc = append(sc, scenario{
			name: fmt.Sprintf("tool stream in %d-byte writes", n),
			req:  toolRequest,
			resp: Response{Steps: toolSteps, ChunkSize: n},
			want: toolEvents,
		})
	}
	return sc
}

func TestConformance(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			var sequences [][]llm.Event
			for _, a := range adapters {
				t.Run(string(a.dialect), func(t *testing.T) {
					srv, p := newProvider(t, a, sc.caps)
					srv.Respond(sc.resp)
					req := baseRequest()
					if sc.req != nil {
						req = sc.req()
					}
					got := collect(t.Context(), t, p, req)
					sequences = append(sequences, normalize(got))
					assertEvents(t, got, sc.want, sc.wantErr)
					for _, ev := range got {
						if f, ok := ev.(llm.Failed); ok {
							assertRedacted(t, f.Err)
						}
					}
					if sc.check != nil {
						sc.check(t, a.dialect, srv)
					}
				})
			}
			if len(sequences) == 2 && !reflect.DeepEqual(sequences[0], sequences[1]) {
				t.Errorf("adapters disagree:\nopenai:    %#v\nanthropic: %#v", sequences[0], sequences[1])
			}
		})
	}
}

func assertEvents(t *testing.T, got, want []llm.Event, wantErr error) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("no events; a stream must end with Completed or Failed")
	}
	last := got[len(got)-1]
	if wantErr == nil {
		if !reflect.DeepEqual(normalize(got), normalize(want)) {
			t.Fatalf("events:\n got %#v\nwant %#v", got, want)
		}
		return
	}
	f, ok := last.(llm.Failed)
	if !ok {
		t.Fatalf("stream ended with %#v, want Failed(%v)", last, wantErr)
	}
	if !errors.Is(f.Err, wantErr) {
		t.Fatalf("Failed(%v), want %v", f.Err, wantErr)
	}
	switch w := wantErr.(type) {
	case *llm.ErrRateLimited:
		var g *llm.ErrRateLimited
		if errors.As(f.Err, &g); g.RetryAfter != w.RetryAfter {
			t.Errorf("RetryAfter = %v, want %v", g.RetryAfter, w.RetryAfter)
		}
	case *llm.ErrProvider:
		var g *llm.ErrProvider
		if errors.As(f.Err, &g); g.Status != w.Status {
			t.Errorf("Status = %d, want %d", g.Status, w.Status)
		}
	case *llm.ErrUnsupportedCapability:
		var g *llm.ErrUnsupportedCapability
		if errors.As(f.Err, &g); g.Cap != w.Cap {
			t.Errorf("Cap = %s, want %s", g.Cap, w.Cap)
		}
	}
	if prefix := normalize(got[:len(got)-1]); !reflect.DeepEqual(prefix, normalize(want)) {
		t.Fatalf("events before failure:\n got %#v\nwant %#v", prefix, want)
	}
}

// normalize makes events comparable: JSON arguments are compacted and
// errors reduced to their normalized class.
func normalize(evs []llm.Event) []llm.Event {
	out := make([]llm.Event, 0, len(evs))
	for _, ev := range evs {
		switch e := ev.(type) {
		case llm.ToolCallCompleted:
			var v any
			json.Unmarshal(e.Arguments, &v)
			e.Arguments, _ = json.Marshal(v)
			ev = e
		case llm.Failed:
			ev = llm.Failed{Err: errors.New(errorClass(e.Err))}
		}
		out = append(out, ev)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func errorClass(err error) string {
	for _, c := range []error{llm.ErrAuth, llm.ErrContextLength, llm.ErrMalformedStream, llm.ErrNetwork, llm.ErrTimeout,
		llm.ErrInvalidRequest, context.Canceled, &llm.ErrRateLimited{}, &llm.ErrUnsupportedCapability{}, &llm.ErrProvider{}} {
		if errors.Is(err, c) {
			return fmt.Sprintf("%T:%v", c, c)
		}
	}
	return "unclassified: " + err.Error()
}

func assertRedacted(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	for _, secret := range []string{apiKey, promptCanary, responseCanary, "invalid key"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error %q leaks %q", msg, secret)
		}
	}
	if errorClass(err)[:12] == "unclassified" {
		t.Errorf("error %v is not a normalized error", err)
	}
}

// TestCancelSlowServer checks that cancelling the context ends a stalled
// stream within 100 ms, both mid-stream and before response headers.
func TestCancelSlowServer(t *testing.T) {
	const budget = 100 * time.Millisecond
	for _, a := range adapters {
		t.Run(string(a.dialect)+"/mid-stream", func(t *testing.T) {
			srv, p := newProvider(t, a, nil)
			srv.Respond(Response{Steps: []Step{Text("first"), Stall()}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s, err := p.Stream(ctx, baseRequest())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if ev, err := s.Recv(); err != nil || !reflect.DeepEqual(ev, llm.TextDelta{Text: "first"}) {
				t.Fatalf("first event %#v, %v", ev, err)
			}
			done := make(chan llm.Event, 1)
			go func() {
				ev, _ := s.Recv()
				done <- ev
			}()
			time.Sleep(20 * time.Millisecond) // let Recv block on the stalled body
			start := time.Now()
			cancel()
			select {
			case ev := <-done:
				f, ok := ev.(llm.Failed)
				if !ok || !errors.Is(f.Err, context.Canceled) {
					t.Fatalf("got %#v, want Failed(context.Canceled)", ev)
				}
				if el := time.Since(start); el > budget {
					t.Fatalf("returned after %v", el)
				}
			case <-time.After(time.Second):
				t.Fatal("Recv did not return after cancel")
			}
			if _, err := s.Recv(); !errors.Is(err, io.EOF) {
				t.Errorf("Recv after Failed = %v, want io.EOF", err)
			}
		})
		t.Run(string(a.dialect)+"/before-headers", func(t *testing.T) {
			srv, p := newProvider(t, a, nil)
			srv.Respond(Response{StallHeaders: true})
			ctx, cancel := context.WithCancel(t.Context())
			started := make(chan time.Time, 1)
			go func() {
				time.Sleep(20 * time.Millisecond)
				started <- time.Now()
				cancel()
			}()
			_, err := p.Stream(ctx, baseRequest())
			if el := time.Since(<-started); !errors.Is(err, context.Canceled) || el > budget {
				t.Fatalf("Stream = %v after %v", err, el)
			}
		})
	}
}

// TestCloseAbortsRecv checks that Close from another goroutine unblocks a
// pending Recv.
func TestCloseAbortsRecv(t *testing.T) {
	for _, a := range adapters {
		t.Run(string(a.dialect), func(t *testing.T) {
			srv, p := newProvider(t, a, nil)
			srv.Respond(Response{Steps: []Step{Stall()}})
			s, err := p.Stream(t.Context(), baseRequest())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := s.Recv()
				done <- err
			}()
			time.Sleep(20 * time.Millisecond)
			s.Close()
			select {
			case err := <-done:
				if !errors.Is(err, io.EOF) {
					t.Fatalf("Recv after Close = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close did not unblock Recv")
			}
		})
	}
}

// TestRegisteredViaNew checks the adapters register themselves and work
// through llm.New, including retries of a transient failure.
func TestRegisteredViaNew(t *testing.T) {
	for _, a := range adapters {
		t.Run(string(a.dialect), func(t *testing.T) {
			srv := NewServer(t, a.dialect)
			kind := llm.KindOpenAI
			if a.dialect == Anthropic {
				kind = llm.KindAnthropic
			}
			p, err := llm.New(llm.ProviderConfig{
				Name: "fake", Kind: kind, BaseURL: srv.BaseURL(), Model: "m",
				Retry: llm.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond},
			}, apiKey, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			srv.Respond(Response{Status: 503, ErrorMessage: leakyMessage})
			got := collect(t.Context(), t, p, baseRequest())
			if n := len(srv.Requests()); n != 2 {
				t.Errorf("%d attempts, want 2", n)
			}
			assertEvents(t, got, nil, &llm.ErrProvider{Status: 503})

			srv.Respond(Response{Status: 401})
			collect(t.Context(), t, p, baseRequest())
			if n := len(srv.Requests()); n != 3 {
				t.Errorf("auth failure retried: %d requests", n)
			}
		})
	}
}
