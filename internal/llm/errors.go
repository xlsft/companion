package llm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Normalized errors. Their messages are built only from status codes,
// provider error codes and capability names, never from the API key,
// prompts or response text, so they are safe to log.
var (
	// ErrAuth: the key was rejected (401/403). Never retried.
	ErrAuth = errors.New("llm: authentication failed")
	// ErrContextLength: the request does not fit the model's context window.
	ErrContextLength = errors.New("llm: context length exceeded")
	// ErrMalformedStream: the response could not be parsed or ended before
	// the provider's end-of-stream marker.
	ErrMalformedStream = errors.New("llm: malformed stream")
	// ErrNetwork: the connection failed or broke.
	ErrNetwork = errors.New("llm: network error")
	// ErrTimeout: no response or no stream data within ProviderConfig.Timeout.
	ErrTimeout = errors.New("llm: provider timed out")
	// ErrInvalidRequest: the ChatRequest itself is malformed.
	ErrInvalidRequest = errors.New("llm: invalid request")
)

// ErrRateLimited: the provider asked to slow down (429, or 529 overloaded).
// RetryAfter is zero when the provider gave no hint.
type ErrRateLimited struct {
	RetryAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("llm: rate limited, retry after %s", e.RetryAfter)
	}
	return "llm: rate limited"
}

// Is makes errors.Is(err, &ErrRateLimited{}) match any rate-limit error.
func (e *ErrRateLimited) Is(target error) bool {
	_, ok := target.(*ErrRateLimited)
	return ok
}

// ErrUnsupportedCapability: the request needs Cap, which the provider (as
// configured) does not offer.
type ErrUnsupportedCapability struct {
	Cap Capability
}

func (e *ErrUnsupportedCapability) Error() string {
	return fmt.Sprintf("llm: provider does not support %s", e.Cap)
}

func (e *ErrUnsupportedCapability) Is(target error) bool {
	_, ok := target.(*ErrUnsupportedCapability)
	return ok
}

// ErrProvider is any other provider-side failure. Code is the provider's
// machine-readable error code or type, sanitized; it may be empty.
type ErrProvider struct {
	Status int // HTTP status; 0 for errors reported inside the stream
	Code   string
}

func (e *ErrProvider) Error() string {
	switch {
	case e.Status != 0 && e.Code != "":
		return fmt.Sprintf("llm: provider error: status %d, code %s", e.Status, e.Code)
	case e.Status != 0:
		return fmt.Sprintf("llm: provider error: status %d", e.Status)
	case e.Code != "":
		return fmt.Sprintf("llm: provider error: code %s", e.Code)
	}
	return "llm: provider error"
}

func (e *ErrProvider) Is(target error) bool {
	_, ok := target.(*ErrProvider)
	return ok
}

// Retryable reports whether repeating the same request may succeed.
func Retryable(err error) bool {
	var p *ErrProvider
	switch {
	case errors.Is(err, &ErrRateLimited{}), errors.Is(err, ErrNetwork), errors.Is(err, ErrTimeout):
		return true
	case errors.As(err, &p):
		return p.Status >= 500 || p.Status == 408 || p.Status == 409
	}
	return false
}

// ErrorKind returns a short, stable, log-safe name for a normalized error:
// the technical code UIs and diagnostics key on. It never includes message
// text.
func ErrorKind(err error) string {
	var p *ErrProvider
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAuth):
		return "auth"
	case errors.Is(err, &ErrRateLimited{}):
		return "rate_limit"
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrNetwork):
		return "network"
	case errors.Is(err, ErrContextLength):
		return "context_length"
	case errors.Is(err, &ErrUnsupportedCapability{}):
		return "unsupported_capability"
	case errors.Is(err, ErrMalformedStream):
		return "malformed_stream"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &p):
		return "provider"
	}
	return "unknown"
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}
