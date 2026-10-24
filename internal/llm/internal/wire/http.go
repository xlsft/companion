package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xlsft/openclick/internal/llm"
)

// maxErrorBody bounds how much of an error response is read for
// classification.
const maxErrorBody = 64 << 10

var errIdle = errors.New("idle timeout")

// Malformed wraps llm.ErrMalformedStream with a fixed, content-free reason.
func Malformed(reason string) error {
	return fmt.Errorf("%w: %s", llm.ErrMalformedStream, reason)
}

// Request is one streaming POST.
type Request struct {
	Client  *http.Client
	URL     string
	Header  http.Header
	Body    any // encoded as JSON
	Timeout time.Duration
	// ParseError maps a non-2xx response to a normalized error. body is
	// truncated to maxErrorBody.
	ParseError func(status int, h http.Header, body []byte) error
}

// Body is a streaming response body. Reads that stall longer than the
// timeout fail with llm.ErrTimeout; cancellation of the caller's context
// ends a pending Read promptly with the context's error; other failures
// become llm.ErrNetwork.
type Body struct {
	parent  context.Context
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	timeout time.Duration
	rc      io.ReadCloser
}

// Open sends r and returns the body once a 2xx event-stream response
// arrived.
func Open(ctx context.Context, r Request) (*Body, error) {
	payload, err := json.Marshal(r.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode request", llm.ErrInvalidRequest)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = llm.DefaultTimeout
	}
	cctx, cancel := context.WithCancelCause(ctx)
	b := &Body{parent: ctx, ctx: cctx, cancel: cancel, timeout: timeout}
	b.timer = time.AfterFunc(timeout, func() { cancel(errIdle) })

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, r.URL, bytes.NewReader(payload))
	if err != nil {
		b.release()
		// The URL error text may contain query parameters; leave it out.
		return nil, fmt.Errorf("%w: cannot build request", llm.ErrInvalidRequest)
	}
	req.Header = r.Header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := r.Client.Do(req)
	if err != nil {
		err = b.normalize(err)
		b.release()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		b.release()
		return nil, r.ParseError(resp.StatusCode, resp.Header, data)
	}
	b.timer.Stop()
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/json" {
		// The server ignored stream:true; treat as a protocol mismatch
		// rather than guessing at a non-streaming body.
		resp.Body.Close()
		b.release()
		return nil, Malformed("expected an event stream, got JSON")
	}
	b.rc = resp.Body
	return b, nil
}

func (b *Body) Read(p []byte) (int, error) {
	b.timer.Reset(b.timeout)
	n, err := b.rc.Read(p)
	b.timer.Stop() // a slow consumer is not an idle server
	if err != nil && !errors.Is(err, io.EOF) {
		err = b.normalize(err)
	}
	return n, err
}

// Close releases the connection and unblocks a pending Read.
func (b *Body) Close() error {
	b.release()
	if b.rc != nil {
		return b.rc.Close()
	}
	return nil
}

func (b *Body) release() {
	b.timer.Stop()
	b.cancel(nil)
}

func (b *Body) normalize(err error) error {
	if errors.Is(context.Cause(b.ctx), errIdle) {
		return llm.ErrTimeout
	}
	if perr := b.parent.Err(); perr != nil {
		return perr
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		// url.Error repeats the full URL; keep only the transport cause.
		err = ue.Err
	}
	return fmt.Errorf("%w: %w", llm.ErrNetwork, err)
}

// StatusError maps an HTTP status to a normalized error. code is the
// provider's machine-readable error code; contextLength reports that the
// adapter recognised a context-window overflow in the error body.
func StatusError(status int, h http.Header, code string, contextLength bool) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llm.ErrAuth
	case status == http.StatusTooManyRequests || status == 529:
		return &llm.ErrRateLimited{RetryAfter: RetryAfter(h)}
	case contextLength:
		return llm.ErrContextLength
	}
	return &llm.ErrProvider{Status: status, Code: SanitizeCode(code)}
}

// RetryAfter parses retry-after-ms (OpenAI) or Retry-After (seconds or
// HTTP date); zero when absent or unparsable.
func RetryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			return time.Duration(ms * float64(time.Millisecond))
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil {
		if s <= 0 {
			return 0
		}
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// SanitizeCode keeps provider error codes to a short identifier so error
// strings cannot carry arbitrary provider text.
func SanitizeCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		if b.Len() >= 64 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MentionsContextLength recognises context-window overflows in provider
// error messages, for servers that do not send a dedicated code. The
// message is inspected only, never propagated.
func MentionsContextLength(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"context length", "context_length", "context window", "prompt is too long", "maximum context", "too many tokens"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
