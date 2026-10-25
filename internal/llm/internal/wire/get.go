package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xlsft/companion/internal/llm"
)

// maxJSONBody bounds a JSON listing; OpenRouter's model list with its
// descriptions and prices runs to a few megabytes.
const maxJSONBody = 32 << 20

// DefaultGetTimeout bounds a Get without its own Timeout.
const DefaultGetTimeout = 30 * time.Second

// Get is one JSON GET, e.g. a model listing.
type Get struct {
	Client *http.Client
	URL    string
	Header http.Header
	// Timeout bounds the whole call, body included; zero means
	// DefaultGetTimeout.
	Timeout    time.Duration
	ParseError func(status int, h http.Header, body []byte) error
}

// GetJSON sends r and decodes a 2xx response into out. Failures are
// normalized like Open's; an undecodable body is ErrMalformedStream.
func GetJSON(ctx context.Context, r Get, out any) error {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultGetTimeout
	}
	cctx, cancel := context.WithTimeoutCause(ctx, timeout, errIdle)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, r.URL, nil)
	if err != nil {
		// The URL error text may contain query parameters; leave it out.
		return fmt.Errorf("%w: cannot build request", llm.ErrInvalidRequest)
	}
	req.Header = r.Header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return normalize(ctx, cctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return r.ParseError(resp.StatusCode, resp.Header, data)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		if cctx.Err() != nil {
			return normalize(ctx, cctx, err)
		}
		return Malformed("undecodable JSON response")
	}
	return nil
}
