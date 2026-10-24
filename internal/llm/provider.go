package llm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Factory builds an adapter. client is already hardened by New.
type Factory func(cfg ProviderConfig, apiKey string, client *http.Client) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[Kind]Factory{}
)

// Register makes an adapter available to New. Adapter packages call it from
// init, so the binary that wires providers imports them for side effects
// (the adapters import this package, so it cannot import them).
func Register(kind Kind, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[kind]; dup {
		panic("llm: adapter registered twice for " + string(kind))
	}
	registry[kind] = f
}

// ErrNoAdapter means no adapter package for the config's Kind is linked in.
var ErrNoAdapter = errors.New("llm: no adapter registered")

// New validates cfg and returns a provider for it, wrapped with the
// config's retry policy. apiKey may be empty for local servers that need
// no key. A nil client means a fresh default client.
func New(cfg ProviderConfig, apiKey string, client *http.Client) (Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	hc, err := hardenClient(client)
	if err != nil {
		return nil, err
	}
	registryMu.RLock()
	f := registry[cfg.Kind]
	registryMu.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoAdapter, cfg.Kind)
	}
	p, err := f(cfg, apiKey, hc)
	if err != nil {
		return nil, err
	}
	return WithRetry(p, cfg.Retry), nil
}

// hardenClient copies client so that redirects are not followed: a
// redirect could forward the key header to another host or downgrade to
// plain http. TLS verification must stay on (FR-BYOK-009).
func hardenClient(client *http.Client) (*http.Client, error) {
	var hc http.Client
	if client != nil {
		hc = *client
	}
	if t, ok := hc.Transport.(*http.Transport); ok && t.TLSClientConfig != nil && t.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("llm: TLS verification must not be disabled")
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc, nil
}

// WithRetry wraps p so that retryable failures of Stream (before any event
// was delivered) are retried with exponential backoff. Rate limits honour
// RetryAfter up to policy.MaxDelay; a longer hint is returned to the caller
// at once so the UI can show it instead of blocking.
func WithRetry(p Provider, policy RetryPolicy) Provider {
	return &retrying{Provider: p, policy: policy.withDefaults(), sleep: sleepCtx}
}

type retrying struct {
	Provider
	policy RetryPolicy
	sleep  func(context.Context, time.Duration) error
}

func (r *retrying) Stream(ctx context.Context, req ChatRequest) (EventStream, error) {
	for attempt := 1; ; attempt++ {
		s, err := r.Provider.Stream(ctx, req)
		if err == nil {
			return s, nil
		}
		if attempt >= r.policy.MaxAttempts || !Retryable(err) || ctx.Err() != nil {
			return nil, err
		}
		d := r.backoff(attempt)
		var rl *ErrRateLimited
		if errors.As(err, &rl) && rl.RetryAfter > 0 {
			if rl.RetryAfter > r.policy.MaxDelay {
				return nil, err
			}
			d = rl.RetryAfter
		}
		if err := r.sleep(ctx, d); err != nil {
			return nil, err
		}
	}
}

func (r *retrying) backoff(attempt int) time.Duration {
	d := r.policy.BaseDelay << (attempt - 1)
	if d <= 0 || d > r.policy.MaxDelay {
		d = r.policy.MaxDelay
	}
	// Jitter in [d/2, d] spreads retries of parallel sessions.
	return d/2 + rand.N(d/2+1)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
