package llm

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/xlsft/openclick/pkg/contracts"
)

// Kind is the wire protocol of a provider.
type Kind string

const (
	KindOpenAI    Kind = "openai-compatible"    // Chat Completions API
	KindAnthropic Kind = "anthropic-compatible" // Messages API
)

// Default base URLs used when ProviderConfig.BaseURL is empty.
const (
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
)

// Defaults applied to zero-valued config fields.
const (
	DefaultTimeout     = 2 * time.Minute
	DefaultMaxAttempts = 3
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 10 * time.Second
)

// RetryPolicy governs retries of failures that happen before streaming
// starts (see Provider.Stream). Zero fields take the defaults above; set
// MaxAttempts to 1 to disable retries.
type RetryPolicy struct {
	MaxAttempts int           // total attempts including the first
	BaseDelay   time.Duration // first backoff, doubled per attempt
	MaxDelay    time.Duration // cap on one backoff and on honoured Retry-After
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultMaxAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = DefaultBaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = DefaultMaxDelay
	}
	return p
}

// ProviderConfig describes one user-configured LLM endpoint (OC-20). The
// API key itself is never part of it; SecretRef points into the OS secret
// store (FR-BYOK-003) and the resolved key is passed to New separately.
type ProviderConfig struct {
	ID      string
	Name    string
	Kind    Kind
	BaseURL string // empty: the official endpoint of Kind
	Model   string
	// Headers are extra request headers, e.g. for gateways. They are stored
	// in plain settings, so credentials must not go here; auth headers are
	// rejected by Validate.
	Headers map[string]string
	// Timeout bounds the wait for response headers and every gap between
	// stream chunks, not the total duration of a long answer. Zero means
	// DefaultTimeout.
	Timeout time.Duration
	Retry   RetryPolicy
	// Capabilities overrides the adapter's defaults per capability.
	Capabilities map[Capability]bool
	SecretRef    contracts.SecretRef
}

// ErrInvalidConfig is wrapped by every *ConfigError.
var ErrInvalidConfig = errors.New("llm: invalid provider config")

// ConfigError names the offending field so the settings UI can highlight it.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrInvalidConfig, e.Field, e.Reason)
}

func (e *ConfigError) Unwrap() error { return ErrInvalidConfig }

func cfgErr(field, reason string) error { return &ConfigError{Field: field, Reason: reason} }

// Headers the adapters own; letting settings override them would either
// leak a credential into plain storage or break the protocol.
var reservedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"x-api-key":           true,
	"api-key":             true,
	"cookie":              true,
	"content-type":        true,
	"content-length":      true,
	"host":                true,
	"accept":              true,
	"transfer-encoding":   true,
	"connection":          true,
}

// Validate checks the config without touching the network.
func (c ProviderConfig) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return cfgErr("Name", "required")
	}
	if c.Kind != KindOpenAI && c.Kind != KindAnthropic {
		return cfgErr("Kind", "unknown provider kind")
	}
	if strings.TrimSpace(c.Model) == "" {
		return cfgErr("Model", "required")
	}
	if _, err := c.Endpoint(); err != nil {
		return err
	}
	for name, value := range c.Headers {
		if !validHeaderName(name) {
			return cfgErr("Headers", "invalid header name")
		}
		if reservedHeaders[strings.ToLower(name)] {
			return cfgErr("Headers", "header "+name+" is managed by the adapter")
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return cfgErr("Headers", "invalid header value for "+name)
		}
	}
	if c.Timeout < 0 {
		return cfgErr("Timeout", "negative")
	}
	if c.Retry.MaxAttempts < 0 || c.Retry.BaseDelay < 0 || c.Retry.MaxDelay < 0 {
		return cfgErr("Retry", "negative")
	}
	for cap := range c.Capabilities {
		if !knownCapability(cap) {
			return cfgErr("Capabilities", "unknown capability "+string(cap))
		}
	}
	return nil
}

// Endpoint returns the validated base URL, applying the Kind default.
//
// Plain http is accepted only for loopback and private-range literal IPs
// (and "localhost"), so a local inference server works without TLS while
// a remote key never travels unencrypted (FR-BYOK-009). Host names are
// not resolved here, so a private host must be given by IP.
func (c ProviderConfig) Endpoint() (*url.URL, error) {
	raw := strings.TrimSpace(c.BaseURL)
	if raw == "" {
		switch c.Kind {
		case KindOpenAI:
			raw = DefaultOpenAIBaseURL
		case KindAnthropic:
			raw = DefaultAnthropicBaseURL
		default:
			return nil, cfgErr("Kind", "unknown provider kind")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" {
		return nil, cfgErr("BaseURL", "not an absolute URL")
	}
	if u.User != nil {
		return nil, cfgErr("BaseURL", "credentials in URL are not allowed")
	}
	if u.Fragment != "" {
		return nil, cfgErr("BaseURL", "fragment not allowed")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocalHost(u.Hostname()) {
			return nil, cfgErr("BaseURL", "https required for non-local hosts")
		}
	default:
		return nil, cfgErr("BaseURL", "scheme must be https or http")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate()
}

// RFC 9110 token characters.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
