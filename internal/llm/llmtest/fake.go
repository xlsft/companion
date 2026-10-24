// Package llmtest provides scripted fake LLM servers that speak the
// OpenAI Chat Completions and Anthropic Messages streaming protocols, and
// the conformance suite that runs both adapters against the same scripts
// (OC-26). Tests never touch the network: servers are httptest loopback
// servers.
package llmtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/xlsft/openclick/internal/llm"
)

// Dialect is a wire protocol a fake server speaks.
type Dialect string

const (
	OpenAI    Dialect = "openai"
	Anthropic Dialect = "anthropic"
)

// StepKind is one abstract action of a scripted response.
type StepKind int

const (
	StepText      StepKind = iota // visible text fragment
	StepThinking                  // reasoning fragment
	StepToolStart                 // open a tool call (ID, Name)
	StepToolArgs                  // fragment of the open call's JSON arguments
	StepToolEnd                   // close the open tool call
	StepFinish                    // stop reason, usage and end-of-stream marker
	StepMalformed                 // an event whose JSON is broken
	StepDrop                      // abort the connection
	StepStall                     // block until the client goes away
)

// Step is rendered differently per dialect; the normalized events an
// adapter derives from it must not depend on the dialect.
type Step struct {
	Kind  StepKind
	Text  string // StepText, StepThinking, StepToolArgs, StepMalformed
	ID    string // StepToolStart
	Name  string // StepToolStart
	Stop  llm.StopReason
	Usage llm.Usage
}

func Text(s string) Step             { return Step{Kind: StepText, Text: s} }
func Think(s string) Step            { return Step{Kind: StepThinking, Text: s} }
func ToolStart(id, name string) Step { return Step{Kind: StepToolStart, ID: id, Name: name} }
func ToolArgs(s string) Step         { return Step{Kind: StepToolArgs, Text: s} }
func ToolEnd() Step                  { return Step{Kind: StepToolEnd} }
func Malformed(s string) Step        { return Step{Kind: StepMalformed, Text: s} }
func Drop() Step                     { return Step{Kind: StepDrop} }
func Stall() Step                    { return Step{Kind: StepStall} }
func Finish(stop llm.StopReason, u llm.Usage) Step {
	return Step{Kind: StepFinish, Stop: stop, Usage: u}
}

// Response scripts one reply. A non-zero Status sends a provider-shaped
// error body with ErrorMessage instead of a stream.
type Response struct {
	Status       int
	Header       http.Header
	ErrorMessage string
	// ContextLength makes the error body report a context-window overflow
	// the way each provider does.
	ContextLength bool
	// StallHeaders blocks before any response is written.
	StallHeaders bool
	Steps        []Step
	// ChunkSize > 0 splits the body into writes of that many bytes, each
	// flushed, so events straddle network reads.
	ChunkSize int
}

// Recorded is one request the server received.
type Recorded struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

// Server is a scripted fake provider.
type Server struct {
	*httptest.Server
	Dialect Dialect

	mu       sync.Mutex
	resp     Response
	requests []Recorded
}

// NewServer starts a fake server for d; it is closed at test cleanup.
func NewServer(t testing.TB, d Dialect) *Server {
	s := &Server{Dialect: d}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Respond sets the reply to all following requests.
func (s *Server) Respond(r Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resp = r
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.requests...)
}

// BaseURL is what a ProviderConfig for this server should use.
func (s *Server) BaseURL() string {
	if s.Dialect == OpenAI {
		return s.URL + "/v1"
	}
	return s.URL
}

func (s *Server) path() string {
	if s.Dialect == OpenAI {
		return "/v1/chat/completions"
	}
	return "/v1/messages"
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body) // draining lets the server notice hang-ups
	rec := Recorded{Path: r.URL.Path, Header: r.Header.Clone()}
	json.Unmarshal(raw, &rec.Body)
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	resp := s.resp
	s.mu.Unlock()

	if r.Method != http.MethodPost || r.URL.Path != s.path() {
		http.NotFound(w, r)
		return
	}
	if resp.StallHeaders {
		<-r.Context().Done()
		return
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	if resp.Status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.Status)
		w.Write(s.errorBody(resp))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher := w.(http.Flusher)
	flusher.Flush()

	var ops []op
	if s.Dialect == OpenAI {
		ops = renderOpenAI(resp.Steps)
	} else {
		ops = renderAnthropic(resp.Steps)
	}
	for _, o := range ops {
		switch {
		case o.drop:
			panic(http.ErrAbortHandler)
		case o.stall:
			<-r.Context().Done()
			return
		}
		for b := o.data; len(b) > 0; {
			n := len(b)
			if resp.ChunkSize > 0 && resp.ChunkSize < n {
				n = resp.ChunkSize
			}
			if _, err := w.Write(b[:n]); err != nil {
				return
			}
			flusher.Flush()
			b = b[n:]
		}
	}
}

func (s *Server) errorBody(r Response) []byte {
	msg := r.ErrorMessage
	if r.ContextLength {
		msg = "prompt is too long: maximum context length exceeded. " + msg
	}
	var body any
	if s.Dialect == OpenAI {
		code := map[int]string{401: "invalid_api_key", 403: "invalid_api_key", 429: "rate_limit_exceeded"}[r.Status]
		if r.ContextLength {
			code = "context_length_exceeded"
		}
		if code == "" {
			code = "server_error"
		}
		body = map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": code}}
	} else {
		typ := map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 429: "rate_limit_error", 529: "overloaded_error"}[r.Status]
		if typ == "" {
			typ = "api_error"
		}
		body = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}}
	}
	b, _ := json.Marshal(body)
	return b
}

type op struct {
	data  []byte
	drop  bool
	stall bool
}

func dataLine(v any) op {
	b, _ := json.Marshal(v)
	return op{data: []byte("data: " + string(b) + "\n\n")}
}

func renderOpenAI(steps []Step) []op {
	var ops []op
	delta := func(d map[string]any) op {
		return dataLine(map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": nil}}})
	}
	ops = append(ops, delta(map[string]any{"role": "assistant", "content": ""}))
	tool := -1
	for _, st := range steps {
		switch st.Kind {
		case StepText:
			ops = append(ops, delta(map[string]any{"content": st.Text}))
		case StepThinking:
			ops = append(ops, delta(map[string]any{"reasoning_content": st.Text}))
		case StepToolStart:
			tool++
			ops = append(ops, delta(map[string]any{"tool_calls": []any{map[string]any{"index": tool, "id": st.ID, "type": "function",
				"function": map[string]any{"name": st.Name, "arguments": ""}}}}))
		case StepToolArgs:
			ops = append(ops, delta(map[string]any{"tool_calls": []any{map[string]any{"index": tool,
				"function": map[string]any{"arguments": st.Text}}}}))
		case StepToolEnd:
		case StepFinish:
			reason := map[llm.StopReason]string{llm.StopEnd: "stop", llm.StopToolUse: "tool_calls", llm.StopMaxTokens: "length", llm.StopContentFilter: "content_filter"}[st.Stop]
			ops = append(ops, dataLine(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}}}))
			u := st.Usage
			ops = append(ops, dataLine(map[string]any{"choices": []any{}, "usage": map[string]any{
				"prompt_tokens": u.InputTokens + u.CacheReadTokens, "completion_tokens": u.OutputTokens,
				"total_tokens":          u.InputTokens + u.CacheReadTokens + u.OutputTokens,
				"prompt_tokens_details": map[string]any{"cached_tokens": u.CacheReadTokens}}}))
			ops = append(ops, op{data: []byte("data: [DONE]\n\n")})
		case StepMalformed:
			ops = append(ops, op{data: []byte(`data: {"choices":[{"index":0,"delta":{"content":"` + st.Text + "\n\n")})
		case StepDrop:
			ops = append(ops, op{drop: true})
		case StepStall:
			ops = append(ops, op{stall: true})
		}
	}
	return ops
}

func renderAnthropic(steps []Step) []op {
	event := func(typ string, v map[string]any) op {
		v["type"] = typ
		b, _ := json.Marshal(v)
		return op{data: []byte("event: " + typ + "\ndata: " + string(b) + "\n\n")}
	}
	var usage llm.Usage
	for _, st := range steps {
		if st.Kind == StepFinish {
			usage = st.Usage
		}
	}
	ops := []op{
		event("message_start", map[string]any{"message": map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
			"content": []any{}, "model": "fake", "stop_reason": nil,
			"usage": map[string]any{"input_tokens": usage.InputTokens, "output_tokens": 1,
				"cache_read_input_tokens": usage.CacheReadTokens, "cache_creation_input_tokens": usage.CacheWriteTokens}}}),
		event("ping", map[string]any{}),
	}
	index, open := -1, ""
	closeBlock := func() {
		if open != "" {
			ops = append(ops, event("content_block_stop", map[string]any{"index": index}))
			open = ""
		}
	}
	ensure := func(kind string, block map[string]any) {
		if open == kind && kind != "tool_use" {
			return
		}
		closeBlock()
		index++
		open = kind
		ops = append(ops, event("content_block_start", map[string]any{"index": index, "content_block": block}))
	}
	for _, st := range steps {
		switch st.Kind {
		case StepText:
			ensure("text", map[string]any{"type": "text", "text": ""})
			ops = append(ops, event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "text_delta", "text": st.Text}}))
		case StepThinking:
			ensure("thinking", map[string]any{"type": "thinking", "thinking": ""})
			ops = append(ops, event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "thinking_delta", "thinking": st.Text}}))
		case StepToolStart:
			ensure("tool_use", map[string]any{"type": "tool_use", "id": st.ID, "name": st.Name, "input": map[string]any{}})
		case StepToolArgs:
			ops = append(ops, event("content_block_delta", map[string]any{"index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": st.Text}}))
		case StepToolEnd:
			closeBlock()
		case StepFinish:
			closeBlock()
			reason := map[llm.StopReason]string{llm.StopEnd: "end_turn", llm.StopToolUse: "tool_use", llm.StopMaxTokens: "max_tokens", llm.StopContentFilter: "refusal"}[st.Stop]
			ops = append(ops, event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": st.Usage.OutputTokens}}))
			ops = append(ops, event("message_stop", map[string]any{}))
		case StepMalformed:
			ops = append(ops, op{data: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":" + strconv.Itoa(index) + `,"delta":{"type":"text_delta","text":"` + st.Text + "\n\n")})
		case StepDrop:
			ops = append(ops, op{drop: true})
		case StepStall:
			ops = append(ops, op{stall: true})
		}
	}
	return ops
}

// RetryAfterHeader is a helper for 429 scripts.
func RetryAfterHeader(d time.Duration) http.Header {
	return http.Header{"Retry-After": {fmt.Sprint(int(d.Seconds()))}}
}
