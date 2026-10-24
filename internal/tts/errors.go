package tts

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrAuth: bad or missing API key. Do not retry.
	ErrAuth = errors.New("tts: authentication failed")
	// ErrRateLimited: back off for Error.RetryAfter (if known) and retry.
	ErrRateLimited = errors.New("tts: rate limited")
	// ErrUnavailable: network failure, 5xx, or a stream cut mid-body. Retryable.
	ErrUnavailable = errors.New("tts: provider unavailable")
	// ErrBadRequest: invalid input or a request the provider rejects. Do not retry.
	ErrBadRequest = errors.New("tts: bad request")
)

// Error carries the failure kind plus transport details. Its message holds
// only the provider name, kind and status: never the key, text or HTTP body.
type Error struct {
	Kind       error // one of the Err* sentinels
	Provider   string
	Status     int           // HTTP status, 0 if none
	RetryAfter time.Duration // set for ErrRateLimited when the server said so
	Cause      error         // underlying transport error, already stripped of URLs
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Kind.Error())
	if e.Provider != "" {
		b.WriteString(" (" + e.Provider + ")")
	}
	if e.Status != 0 {
		b.WriteString(" status " + strconv.Itoa(e.Status))
	}
	return b.String()
}

func (e *Error) Is(target error) bool { return target == e.Kind }
func (e *Error) Unwrap() error        { return e.Cause }

func badRequest(msg string) error {
	return fmt.Errorf("%w: %s", ErrBadRequest, msg)
}

// statusError maps a non-2xx response. The body is intentionally ignored.
func statusError(provider string, resp *http.Response) error {
	e := &Error{Provider: provider, Status: resp.StatusCode}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		e.Kind = ErrAuth
	case resp.StatusCode == 429:
		e.Kind = ErrRateLimited
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case resp.StatusCode == 408 || resp.StatusCode >= 500:
		e.Kind = ErrUnavailable
	default:
		e.Kind = ErrBadRequest
	}
	return e
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
