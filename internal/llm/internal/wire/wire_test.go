package wire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/xlsft/companion/internal/llm"
)

func readAll(t *testing.T, r io.Reader) ([]SSEEvent, error) {
	t.Helper()
	s := NewSSEReader(r)
	var out []SSEEvent
	for {
		ev, err := s.Next()
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

func TestSSEReader(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []SSEEvent
	}{
		{"single", "data: a\n\n", []SSEEvent{{Data: []byte("a")}}},
		{"named", "event: ping\ndata: {}\n\n", []SSEEvent{{Event: "ping", Data: []byte("{}")}}},
		{"crlf", "event: x\r\ndata: 1\r\n\r\n", []SSEEvent{{Event: "x", Data: []byte("1")}}},
		{"multiline", "data: a\ndata:b\ndata\n\n", []SSEEvent{{Data: []byte("a\nb\n")}}},
		{"comments and blanks", ": keep-alive\n\n\ndata: x\nid: 7\nretry: 10\n\n", []SSEEvent{{Data: []byte("x")}}},
		{"no trailing blank line", "data: a\n\ndata: [DONE]", []SSEEvent{{Data: []byte("a")}, {Data: []byte("[DONE]")}}},
		{"no space after colon", "data:x\n\n", []SSEEvent{{Data: []byte("x")}}},
	}
	for _, tt := range tests {
		for _, oneByte := range []bool{false, true} {
			t.Run(tt.name, func(t *testing.T) {
				var r io.Reader = strings.NewReader(tt.in)
				if oneByte {
					r = iotest.OneByteReader(r)
				}
				got, err := readAll(t, r)
				if !errors.Is(err, io.EOF) {
					t.Fatalf("err = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("got %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestSSEReaderLongLine(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	got, err := readAll(t, strings.NewReader("data: "+long+"\n\n"))
	if !errors.Is(err, io.EOF) || len(got) != 1 || string(got[0].Data) != long {
		t.Fatalf("long line: %d events, err %v", len(got), err)
	}
	_, err = readAll(t, strings.NewReader("data: "+strings.Repeat("x", MaxEventSize+10)+"\n\n"))
	if !errors.Is(err, llm.ErrMalformedStream) {
		t.Fatalf("oversized: %v", err)
	}
}

func TestSSEReaderPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	_, err := readAll(t, io.MultiReader(strings.NewReader("data: a\n\ndata: b"), iotest.ErrReader(boom)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{}, 0},
		{http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{http.Header{"Retry-After": {"1.5"}}, 1500 * time.Millisecond},
		{http.Header{"Retry-After": {"junk"}}, 0},
		{http.Header{"Retry-After": {"-3"}}, 0},
		{http.Header{"Retry-After-Ms": {"250"}, "Retry-After": {"7"}}, 250 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := RetryAfter(tt.h); got != tt.want {
			t.Errorf("RetryAfter(%v) = %v, want %v", tt.h, got, tt.want)
		}
	}
	future := time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)
	if d := RetryAfter(http.Header{"Retry-After": {future}}); d < 50*time.Second || d > time.Minute {
		t.Errorf("http date: %v", d)
	}
}

func TestStatusError(t *testing.T) {
	tests := []struct {
		status int
		ctxLen bool
		want   error
	}{
		{401, false, llm.ErrAuth},
		{403, false, llm.ErrAuth},
		{429, false, &llm.ErrRateLimited{}},
		{529, false, &llm.ErrRateLimited{}},
		{400, true, llm.ErrContextLength},
		{400, false, &llm.ErrProvider{}},
		{500, false, &llm.ErrProvider{}},
	}
	for _, tt := range tests {
		if err := StatusError(tt.status, http.Header{}, "c", tt.ctxLen); !errors.Is(err, tt.want) {
			t.Errorf("StatusError(%d) = %v, want %v", tt.status, err, tt.want)
		}
	}
	if got := SanitizeCode("bad code: sk-123 <script>" + strings.Repeat("a", 100)); strings.ContainsAny(got, " :<>") || len(got) > 64 {
		t.Errorf("SanitizeCode = %q", got)
	}
}

func parseErr(status int, h http.Header, body []byte) error {
	return StatusError(status, h, "", false)
}

func TestOpenTimeouts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Draining the body lets the server notice the client hanging up.
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/stall-headers":
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/stall-body":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		case "/401":
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	defer close(release)
	req := func(path string) Request {
		return Request{Client: srv.Client(), URL: srv.URL + path, Body: map[string]any{}, Timeout: 50 * time.Millisecond, ParseError: parseErr}
	}

	if _, err := Open(context.Background(), req("/stall-headers")); !errors.Is(err, llm.ErrTimeout) {
		t.Errorf("header timeout: %v", err)
	}
	b, err := Open(context.Background(), req("/stall-body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(b); !errors.Is(err, llm.ErrTimeout) {
		t.Errorf("idle timeout: %v", err)
	}
	if _, err := Open(context.Background(), req("/json")); !errors.Is(err, llm.ErrMalformedStream) {
		t.Errorf("json body: %v", err)
	}
	if _, err := Open(context.Background(), req("/401")); !errors.Is(err, llm.ErrAuth) {
		t.Errorf("401: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, req("/stall-headers")); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	r := req("/x")
	r.URL = "http://127.0.0.1:1/?key=SECRET"
	if _, err := Open(context.Background(), r); !errors.Is(err, llm.ErrNetwork) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("dial error: %v", err)
	}
}
