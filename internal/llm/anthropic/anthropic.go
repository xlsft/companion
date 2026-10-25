// Package anthropic is the adapter for Anthropic-compatible Messages API
// endpoints. Importing it registers the adapter for llm.KindAnthropic.
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

func init() {
	llm.Register(llm.KindAnthropic, func(cfg llm.ProviderConfig, apiKey string, hc *http.Client) (llm.Provider, error) {
		return New(cfg, apiKey, hc)
	})
}

// APIVersion is sent as anthropic-version.
const APIVersion = "2023-06-01"

// DefaultMaxTokens is used when the policy sets no output limit; the API
// requires one. It is modest so that compatible servers with small output
// caps accept it.
const DefaultMaxTokens = 8192

// DefaultCapabilities are assumed unless the config overrides them.
var DefaultCapabilities = llm.Capabilities{Vision: true, Tools: true, Thinking: true, ResponseSchema: true}

// Provider talks to POST {BaseURL}/v1/messages.
type Provider struct {
	cfg    llm.ProviderConfig
	apiKey string
	client *http.Client
	url    string
	caps   llm.Capabilities
}

// New builds the adapter. Prefer llm.New, which also validates the config,
// hardens the client and adds retries.
func New(cfg llm.ProviderConfig, apiKey string, client *http.Client) (*Provider, error) {
	u, err := apiURL(cfg, "messages")
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Provider{
		cfg:    cfg,
		apiKey: apiKey,
		client: client,
		url:    u.String(),
		caps:   DefaultCapabilities.With(cfg.Capabilities),
	}, nil
}

// apiURL is {BaseURL}/v1/{path}. The official base URL has no /v1, but
// users often paste one with it.
func apiURL(cfg llm.ProviderConfig, path string) (*url.URL, error) {
	u, err := cfg.Endpoint()
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(u.Path, "/v1") {
		return u.JoinPath(path), nil
	}
	return u.JoinPath("v1", path), nil
}

// headers are the request headers every call sends.
func headers(cfg llm.ProviderConfig, apiKey string) http.Header {
	h := http.Header{}
	for k, v := range cfg.Headers {
		h.Set(k, v)
	}
	h.Set("anthropic-version", APIVersion)
	if apiKey != "" {
		h.Set("x-api-key", apiKey)
	}
	return h
}

func (p *Provider) Capabilities() llm.Capabilities { return p.caps }

func (p *Provider) Stream(ctx context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	if err := llm.CheckRequest(p.caps, req); err != nil {
		return nil, err
	}
	h := headers(p.cfg, p.apiKey)
	body, err := wire.Open(ctx, wire.Request{
		Client:     p.client,
		URL:        p.url,
		Header:     h,
		Body:       buildRequest(p.cfg.Model, req),
		Timeout:    p.cfg.Timeout,
		ParseError: parseError,
	})
	if err != nil {
		return nil, err
	}
	return wire.NewStream(body, newDecoder()), nil
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func parseError(status int, h http.Header, body []byte) error {
	var env struct {
		Error apiError `json:"error"`
	}
	_ = json.Unmarshal(body, &env) // a non-JSON body still maps by status
	e := env.Error
	ctxLen := status == http.StatusBadRequest && wire.MentionsContextLength(e.Message)
	return wire.StatusError(status, h, e.Type, ctxLen)
}

// streamError classifies an "error" event sent after the stream started.
func streamError(e apiError) error {
	switch e.Type {
	case "overloaded_error", "rate_limit_error":
		return &llm.ErrRateLimited{}
	case "authentication_error", "permission_error":
		return llm.ErrAuth
	case "invalid_request_error":
		if wire.MentionsContextLength(e.Message) {
			return llm.ErrContextLength
		}
	}
	return &llm.ErrProvider{Code: wire.SanitizeCode(e.Type)}
}
