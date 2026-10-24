package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

func TestBuildRequest(t *testing.T) {
	temp := 0.2
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.Part{llm.Text("be brief")}},
			{Role: llm.RoleUser, Content: []llm.Part{llm.Text("what is this?")}},
			{Role: llm.RoleAssistant, Content: []llm.Part{llm.Thinking("hmm", "sig"), llm.Text("let me look")}, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "screenshot"}}},
			{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.Part{llm.Text("ok"), llm.ImagePart(llm.Image{MediaType: llm.MediaPNG, Data: []byte{1, 2}})}},
			{Role: llm.RoleTool, ToolCallID: "c2", IsError: true, Content: []llm.Part{llm.Text("boom")}},
			{Role: llm.RoleUser, Content: []llm.Part{llm.Text("and now?")}},
		},
		Attachments:    []llm.Image{{MediaType: llm.MediaJPEG, Data: []byte{3}}},
		Tools:          []llm.Tool{{Name: "screenshot", Description: "grab"}},
		Policy:         llm.ModelPolicy{MaxOutputTokens: 100, Temperature: &temp, ToolChoice: llm.ToolChoiceRequired, Effort: "low"},
		ResponseSchema: &llm.ResponseSchema{Schema: json.RawMessage(`{"type":"object"}`)},
	}
	b, _ := json.Marshal(buildRequest("m", req))
	var got map[string]any
	json.Unmarshal(b, &got)
	want := map[string]any{
		"model":                 "m",
		"stream":                true,
		"stream_options":        map[string]any{"include_usage": true},
		"max_completion_tokens": 100.0,
		"temperature":           0.2,
		"reasoning_effort":      "low",
		"tool_choice":           "required",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "screenshot", "description": "grab", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": map[string]any{"type": "object"}}},
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "what is this?"},
			map[string]any{"role": "assistant", "content": "let me look", "tool_calls": []any{map[string]any{
				"id": "c1", "type": "function", "function": map[string]any{"name": "screenshot", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": "ok"},
			map[string]any{"role": "tool", "tool_call_id": "c2", "content": "Error: boom"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Image output of tool call c1:"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AQI="}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "and now?"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,Aw=="}},
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("request mismatch:\n%s", gb)
	}
}

func TestParseError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth", 401, `{"error":{"message":"bad key sk-123","type":"invalid_request_error","code":"invalid_api_key"}}`, llm.ErrAuth},
		{"ctx code", 400, `{"error":{"message":"x","code":"context_length_exceeded"}}`, llm.ErrContextLength},
		{"ctx message", 400, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`, llm.ErrContextLength},
		{"string error", 400, `{"error":"prompt is too long"}`, llm.ErrContextLength},
		{"quota", 429, `{"error":{"code":"insufficient_quota"}}`, &llm.ErrProvider{}},
		{"rate", 429, `{"error":{"code":"rate_limit_exceeded"}}`, &llm.ErrRateLimited{}},
		{"numeric code", 500, `{"error":{"code":500,"type":"server_error"}}`, &llm.ErrProvider{}},
		{"html", 502, `<html>bad gateway</html>`, &llm.ErrProvider{}},
	}
	for _, tt := range tests {
		err := parseError(tt.status, http.Header{}, []byte(tt.body))
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
	var pe *llm.ErrProvider
	if err := parseError(500, http.Header{}, []byte(`{"error":{"code":500,"type":"server_error"}}`)); !errors.As(err, &pe) || pe.Code != "server_error" {
		t.Errorf("code fallback to type: %v", err)
	}
	if err := parseError(429, http.Header{}, []byte(`{"error":{"code":"insufficient_quota"}}`)); llm.Retryable(err) {
		t.Error("insufficient quota must not be retried")
	}
}

func decodeAll(t *testing.T, lines ...string) ([]llm.Event, error) {
	t.Helper()
	d := newDecoder()
	var out []llm.Event
	for _, l := range lines {
		evs, _, err := d.Decode(wire.SSEEvent{Data: []byte(l)})
		out = append(out, evs...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func TestDecoderQuirks(t *testing.T) {
	got, err := decodeAll(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"think"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`,
		`[DONE]`,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []llm.Event{
		llm.ThinkingDelta{Text: "think"},
		llm.ToolCallStarted{Index: 0, ID: "call_0", Name: "f"},
		llm.ToolCallDelta{Index: 0, ArgumentsDelta: `{"a":1}`},
		llm.ToolCallCompleted{Index: 0, ID: "call_0", Name: "f", Arguments: json.RawMessage(`{"a":1}`)},
		llm.Usage{InputTokens: 6, OutputTokens: 5, CacheReadTokens: 4},
		llm.Completed{StopReason: llm.StopToolUse},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}

	_, err = decodeAll(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"g"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
	)
	if !errors.Is(err, llm.ErrMalformedStream) {
		t.Errorf("interleaved: %v", err)
	}

	_, err = decodeAll(t,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{\"a\""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
	)
	if !errors.Is(err, llm.ErrMalformedStream) {
		t.Errorf("truncated arguments: %v", err)
	}

	_, err = decodeAll(t, `{"error":{"message":"overloaded","code":"rate_limit_exceeded"}}`)
	if !errors.Is(err, &llm.ErrRateLimited{}) {
		t.Errorf("in-stream error: %v", err)
	}
}

func TestBudgetThinkingUnsupported(t *testing.T) {
	p, err := New(llm.ProviderConfig{Name: "x", Kind: llm.KindOpenAI, Model: "m", Capabilities: map[llm.Capability]bool{llm.CapThinking: true}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{llm.Text("hi")}}},
		Policy:   llm.ModelPolicy{Thinking: llm.ThinkingBudget, ThinkingBudget: 1024},
	}
	var ue *llm.ErrUnsupportedCapability
	if _, err := p.Stream(t.Context(), req); !errors.As(err, &ue) || ue.Cap != llm.CapThinking {
		t.Fatalf("err = %v", err)
	}
	if p.url != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("url = %s", p.url)
	}
}
