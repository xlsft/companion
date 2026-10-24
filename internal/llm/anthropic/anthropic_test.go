package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/llm/internal/wire"
)

func TestBuildRequest(t *testing.T) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.Part{llm.Text("be brief")}},
			{Role: llm.RoleUser, Content: []llm.Part{llm.Text("what is this?")}},
			{Role: llm.RoleAssistant, Content: []llm.Part{llm.Text("let me look"), llm.Thinking("hmm", "sig")}, ToolCalls: []llm.ToolCall{
				{ID: "c1", Name: "screenshot"}, {ID: "c2", Name: "click", Arguments: json.RawMessage(`{"x":1}`)}}},
			{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.Part{llm.ImagePart(llm.Image{MediaType: llm.MediaPNG, Data: []byte{1, 2}})}},
			{Role: llm.RoleTool, ToolCallID: "c2", IsError: true, Content: []llm.Part{llm.Text("boom")}},
			{Role: llm.RoleUser, Content: []llm.Part{llm.Text("and now?")}},
		},
		Attachments:    []llm.Image{{MediaType: llm.MediaJPEG, Data: []byte{3}}},
		Tools:          []llm.Tool{{Name: "screenshot", Description: "grab"}},
		Policy:         llm.ModelPolicy{Thinking: llm.ThinkingBudget, ThinkingBudget: 2000, ToolChoice: llm.ToolChoiceRequired, Effort: "high"},
		ResponseSchema: &llm.ResponseSchema{Schema: json.RawMessage(`{"type":"object"}`)},
	}
	b, _ := json.Marshal(buildRequest("m", req))
	var got map[string]any
	json.Unmarshal(b, &got)
	img := func(mt, data string) map[string]any {
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}}
	}
	text := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
	want := map[string]any{
		"model":       "m",
		"max_tokens":  float64(DefaultMaxTokens + 2000),
		"stream":      true,
		"system":      []any{text("be brief")},
		"tool_choice": map[string]any{"type": "any"},
		"thinking":    map[string]any{"type": "enabled", "budget_tokens": 2000.0},
		"output_config": map[string]any{"effort": "high", "format": map[string]any{
			"type": "json_schema", "schema": map[string]any{"type": "object"}}},
		"tools": []any{map[string]any{"name": "screenshot", "description": "grab",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{text("what is this?")}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "hmm", "signature": "sig"},
				text("let me look"),
				map[string]any{"type": "tool_use", "id": "c1", "name": "screenshot", "input": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "c2", "name": "click", "input": map[string]any{"x": 1.0}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": []any{img("image/png", "AQI=")}},
				map[string]any{"type": "tool_result", "tool_use_id": "c2", "is_error": true, "content": []any{text("boom")}},
				text("and now?"),
				img("image/jpeg", "Aw=="),
			}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("request mismatch:\n%s", gb)
	}
}

func TestBuildRequestAdaptive(t *testing.T) {
	req := llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Part{llm.Text("hi")}}},
		Policy:   llm.ModelPolicy{Thinking: llm.ThinkingAdaptive, MaxOutputTokens: 500},
	}
	r := buildRequest("m", req)
	if r.Thinking == nil || r.Thinking.Type != "adaptive" || r.Thinking.BudgetTokens != 0 || r.MaxTokens != 500 || r.OutputConfig != nil || r.Temperature != nil {
		t.Fatalf("%+v", r)
	}
}

func TestURL(t *testing.T) {
	for base, want := range map[string]string{
		"":                             "https://api.anthropic.com/v1/messages",
		"https://gw.example/anthropic": "https://gw.example/anthropic/v1/messages",
		"https://gw.example/v1/":       "https://gw.example/v1/messages",
	} {
		p, err := New(llm.ProviderConfig{Name: "a", Kind: llm.KindAnthropic, Model: "m", BaseURL: base}, "", nil)
		if err != nil || p.url != want {
			t.Errorf("%q: %v %v, want %s", base, p, err, want)
		}
	}
}

func TestParseError(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, llm.ErrAuth},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, llm.ErrContextLength},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad field"}}`, &llm.ErrProvider{}},
		{413, `{"type":"error","error":{"type":"request_too_large"}}`, &llm.ErrProvider{}},
		{529, `{"type":"error","error":{"type":"overloaded_error"}}`, &llm.ErrRateLimited{}},
		{500, `garbage`, &llm.ErrProvider{}},
	}
	for _, tt := range tests {
		if err := parseError(tt.status, http.Header{}, []byte(tt.body)); !errors.Is(err, tt.want) {
			t.Errorf("%d %s: %v, want %v", tt.status, tt.body, err, tt.want)
		}
	}
}

func TestDecoderQuirks(t *testing.T) {
	d := newDecoder()
	var got []llm.Event
	for _, l := range []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"x"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"ping"}`,
		`{"type":"some_future_event"}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	} {
		evs, _, err := d.Decode(wire.SSEEvent{Data: []byte(l)})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, evs...)
	}
	want := []llm.Event{
		llm.ThinkingDelta{Text: "hm"},
		llm.ThinkingDelta{Signature: "SIG"},
		llm.Usage{InputTokens: 10, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2},
		llm.Completed{StopReason: llm.StopContentFilter},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}

	if _, _, err := newDecoder().Decode(wire.SSEEvent{Data: []byte(`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"x"}}`)}); !errors.Is(err, llm.ErrMalformedStream) {
		t.Errorf("unknown block: %v", err)
	}
	if _, _, err := newDecoder().Decode(wire.SSEEvent{Event: "error", Data: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)}); !errors.Is(err, &llm.ErrRateLimited{}) {
		t.Errorf("error event: %v", err)
	}
}
