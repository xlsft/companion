package tts

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// base holds what HTTP providers share: the client, config and the registry
// of live streams that Cancel aborts.
type base struct {
	name   string
	cfg    Config
	client *http.Client

	mu     sync.Mutex
	nextID int
	live   map[int]context.CancelFunc
}

func newBase(name string, cfg Config, client *http.Client) base {
	if client == nil {
		// No client-level Timeout: it would also cap the streaming body.
		client = &http.Client{}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return base{name: name, cfg: cfg, client: client, live: map[int]context.CancelFunc{}}
}

func (b *base) track(cancel context.CancelFunc) (untrack func()) {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.live[id] = cancel
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.live, id)
		b.mu.Unlock()
	}
}

func (b *base) Cancel(context.Context) error {
	b.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(b.live))
	for _, c := range b.live {
		cancels = append(cancels, c)
	}
	b.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return nil
}

// do sends req and returns the response only for 2xx. The timeout bounds the
// wait for headers; the body is left to the caller and runs under ctx only.
func (b *base) do(ctx context.Context, cancel context.CancelFunc, req *http.Request) (*http.Response, error) {
	for k, v := range b.cfg.Headers {
		req.Header.Set(k, v)
	}
	var mu sync.Mutex
	fired := false
	t := time.AfterFunc(b.cfg.timeout(), func() {
		mu.Lock()
		fired = true
		mu.Unlock()
		cancel()
	})
	resp, err := b.client.Do(req.WithContext(ctx))
	t.Stop()
	mu.Lock()
	timedOut := fired
	mu.Unlock()
	if err != nil {
		if timedOut {
			return nil, &Error{Kind: ErrUnavailable, Provider: b.name, Cause: errors.New("timeout waiting for response")}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: ErrUnavailable, Provider: b.name, Cause: stripURL(err)}
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, statusError(b.name, resp)
	}
	return resp, nil
}

// stripURL drops the *url.Error wrapper so the request URL (which may carry
// user-supplied host details) does not leak into messages.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
