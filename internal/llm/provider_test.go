package llm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type scriptedProvider struct {
	errs  []error // returned by successive Stream calls; nil means success
	calls int
}

func (p *scriptedProvider) Stream(context.Context, ChatRequest) (EventStream, error) {
	i := p.calls
	p.calls++
	if i < len(p.errs) && p.errs[i] != nil {
		return nil, p.errs[i]
	}
	return nil, nil
}

func (p *scriptedProvider) Capabilities() Capabilities { return Capabilities{} }

func TestRetry(t *testing.T) {
	rl := func(d time.Duration) error { return &ErrRateLimited{RetryAfter: d} }
	tests := []struct {
		name      string
		errs      []error
		attempts  int
		wantCalls int
		wantErr   error
		wantSleep []time.Duration // exact sleeps; nil: not checked
	}{
		{"success", nil, 3, 1, nil, nil},
		{"auth never retried", []error{ErrAuth}, 3, 1, ErrAuth, nil},
		{"context length never retried", []error{ErrContextLength}, 3, 1, ErrContextLength, nil},
		{"unsupported never retried", []error{&ErrUnsupportedCapability{Cap: CapVision}}, 3, 1, &ErrUnsupportedCapability{}, nil},
		{"4xx not retried", []error{&ErrProvider{Status: 400}}, 3, 1, &ErrProvider{}, nil},
		{"5xx retried", []error{&ErrProvider{Status: 503}, nil}, 3, 2, nil, nil},
		{"network retried", []error{ErrNetwork, ErrTimeout, nil}, 3, 3, nil, nil},
		{"gives up", []error{ErrNetwork, ErrNetwork, ErrNetwork, nil}, 3, 3, ErrNetwork, nil},
		{"retry-after honoured", []error{rl(3 * time.Second), nil}, 3, 2, nil, []time.Duration{3 * time.Second}},
		{"retry-after too long", []error{rl(time.Hour), nil}, 3, 1, &ErrRateLimited{}, []time.Duration{}},
		{"single attempt", []error{ErrNetwork, nil}, 1, 1, ErrNetwork, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := &scriptedProvider{errs: tt.errs}
			r := WithRetry(sp, RetryPolicy{MaxAttempts: tt.attempts, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Second}).(*retrying)
			slept := []time.Duration{}
			r.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
			_, err := r.Stream(context.Background(), ChatRequest{})
			if sp.calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", sp.calls, tt.wantCalls)
			}
			if (tt.wantErr == nil) != (err == nil) || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantSleep != nil && !equalDurations(slept, tt.wantSleep) {
				t.Errorf("slept %v, want %v", slept, tt.wantSleep)
			}
		})
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sp := &scriptedProvider{errs: []error{ErrNetwork, ErrNetwork, ErrNetwork}}
	r := WithRetry(sp, RetryPolicy{MaxAttempts: 3, BaseDelay: time.Hour, MaxDelay: time.Hour})
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := r.Stream(ctx, ChatRequest{})
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestBackoffBounds(t *testing.T) {
	r := WithRetry(&scriptedProvider{}, RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}).(*retrying)
	for attempt := 1; attempt < 70; attempt++ {
		d := r.backoff(attempt)
		if d <= 0 || d > time.Second {
			t.Fatalf("backoff(%d) = %v", attempt, d)
		}
	}
}

func TestCheckRequest(t *testing.T) {
	user := Message{Role: RoleUser, Content: []Part{Text("hi")}}
	png := Image{MediaType: MediaPNG, Data: []byte{1}}
	all := Capabilities{Vision: true, Tools: true, Thinking: true, ResponseSchema: true}
	schema := json.RawMessage(`{"type":"object"}`)

	tests := []struct {
		name string
		caps Capabilities
		req  ChatRequest
		want error // nil, ErrInvalidRequest or *ErrUnsupportedCapability{Cap}
	}{
		{"minimal", Capabilities{}, ChatRequest{Messages: []Message{user}}, nil},
		{"empty", all, ChatRequest{}, ErrInvalidRequest},
		{"system first", all, ChatRequest{Messages: []Message{{Role: RoleSystem, Content: []Part{Text("s")}}, user}}, nil},
		{"system late", all, ChatRequest{Messages: []Message{user, {Role: RoleSystem, Content: []Part{Text("s")}}}}, ErrInvalidRequest},
		{"bad role", all, ChatRequest{Messages: []Message{{Role: "bot", Content: []Part{Text("x")}}}}, ErrInvalidRequest},
		{"tool without id", all, ChatRequest{Messages: []Message{user, {Role: RoleTool, Content: []Part{Text("r")}}}}, ErrInvalidRequest},
		{"id on user", all, ChatRequest{Messages: []Message{{Role: RoleUser, ToolCallID: "c", Content: []Part{Text("x")}}}}, ErrInvalidRequest},
		{"bad tool call args", all, ChatRequest{Messages: []Message{user, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "f", Arguments: json.RawMessage(`[1]`)}}}}}, ErrInvalidRequest},
		{"gif", all, ChatRequest{Messages: []Message{user}, Attachments: []Image{{MediaType: "image/gif", Data: []byte{1}}}}, ErrInvalidRequest},
		{"image in assistant", all, ChatRequest{Messages: []Message{user, {Role: RoleAssistant, Content: []Part{ImagePart(png)}}}}, ErrInvalidRequest},
		{"vision ok", all, ChatRequest{Messages: []Message{user}, Attachments: []Image{png}}, nil},
		{"no vision", Capabilities{}, ChatRequest{Messages: []Message{user}, Attachments: []Image{png}}, &ErrUnsupportedCapability{Cap: CapVision}},
		{"no vision tool result", Capabilities{Tools: true}, ChatRequest{Messages: []Message{user, {Role: RoleTool, ToolCallID: "c", Content: []Part{ImagePart(png)}}}}, &ErrUnsupportedCapability{Cap: CapVision}},
		{"no tools", Capabilities{}, ChatRequest{Messages: []Message{user}, Tools: []Tool{{Name: "f"}}}, &ErrUnsupportedCapability{Cap: CapTools}},
		{"bad tool name", all, ChatRequest{Messages: []Message{user}, Tools: []Tool{{Name: "a b"}}}, ErrInvalidRequest},
		{"dup tool", all, ChatRequest{Messages: []Message{user}, Tools: []Tool{{Name: "f"}, {Name: "f"}}}, ErrInvalidRequest},
		{"bad tool schema", all, ChatRequest{Messages: []Message{user}, Tools: []Tool{{Name: "f", Schema: json.RawMessage(`"x"`)}}}, ErrInvalidRequest},
		{"required no tools", all, ChatRequest{Messages: []Message{user}, Policy: ModelPolicy{ToolChoice: ToolChoiceRequired}}, ErrInvalidRequest},
		{"no thinking", Capabilities{}, ChatRequest{Messages: []Message{user}, Policy: ModelPolicy{Thinking: ThinkingAdaptive}}, &ErrUnsupportedCapability{Cap: CapThinking}},
		{"no effort", Capabilities{}, ChatRequest{Messages: []Message{user}, Policy: ModelPolicy{Effort: "low"}}, &ErrUnsupportedCapability{Cap: CapThinking}},
		{"budget zero", all, ChatRequest{Messages: []Message{user}, Policy: ModelPolicy{Thinking: ThinkingBudget}}, ErrInvalidRequest},
		{"no schema", Capabilities{}, ChatRequest{Messages: []Message{user}, ResponseSchema: &ResponseSchema{Name: "r", Schema: schema}}, &ErrUnsupportedCapability{Cap: CapResponseSchema}},
		{"bad schema", all, ChatRequest{Messages: []Message{user}, ResponseSchema: &ResponseSchema{Name: "r"}}, ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckRequest(tt.caps, tt.req)
			switch want := tt.want.(type) {
			case nil:
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			case *ErrUnsupportedCapability:
				var got *ErrUnsupportedCapability
				if !errors.As(err, &got) || got.Cap != want.Cap {
					t.Fatalf("err = %v, want unsupported %s", err, want.Cap)
				}
			default:
				if !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v", err, want)
				}
			}
		})
	}
}

func TestWithAttachments(t *testing.T) {
	img := Image{MediaType: MediaJPEG, Data: []byte{9}}
	req := ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: []Part{Text("a")}},
			{Role: RoleAssistant, Content: []Part{Text("b")}},
			{Role: RoleUser, Content: []Part{Text("c")}},
		},
		Attachments: []Image{img},
	}
	got := req.WithAttachments()
	if len(got[2].Content) != 2 || got[2].Content[1].Image.MediaType != MediaJPEG {
		t.Fatalf("attachment not merged into last user message: %+v", got[2])
	}
	if len(req.Messages[2].Content) != 1 {
		t.Fatal("WithAttachments mutated the request")
	}
}

func TestErrorMatching(t *testing.T) {
	err := error(&ErrRateLimited{RetryAfter: time.Second})
	if !errors.Is(err, &ErrRateLimited{}) || !Retryable(err) {
		t.Error("rate limit must match and be retryable")
	}
	if Retryable(ErrAuth) || Retryable(ErrMalformedStream) || Retryable(context.Canceled) {
		t.Error("non-retryable error classified retryable")
	}
	if (&ErrProvider{Status: 500, Code: "server_error"}).Error() != "llm: provider error: status 500, code server_error" {
		t.Error("unexpected provider error text")
	}
}
