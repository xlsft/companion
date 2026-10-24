// Package openai is the adapter for OpenAI-compatible Chat Completions
// endpoints (OpenAI, OpenRouter, vLLM, Ollama, LM Studio, ...). Importing
// it registers the adapter for llm.KindOpenAI.
package openai

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

func init() {
	llm.Register(llm.KindOpenAI, func(cfg llm.ProviderConfig, apiKey string, hc *http.Client) (llm.Provider, error) {
		return New(cfg, apiKey, hc)
	})
}

// DefaultCapabilities are assumed unless the config overrides them.
// Thinking is off because Chat Completions has no portable reasoning
// switch; enable it for models that accept reasoning_effort.
var DefaultCapabilities = llm.Capabilities{Vision: true, Tools: true, ResponseSchema: true}

// Provider talks to POST {BaseURL}/chat/completions.
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
	u, err := cfg.Endpoint()
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
		url:    u.JoinPath("chat/completions").String(),
		caps:   DefaultCapabilities.With(cfg.Capabilities),
	}, nil
}

func (p *Provider) Capabilities() llm.Capabilities { return p.caps }

func (p *Provider) Stream(ctx context.Context, req llm.ChatRequest) (llm.EventStream, error) {
	if err := llm.CheckRequest(p.caps, req); err != nil {
		return nil, err
	}
	if req.Policy.Thinking == llm.ThinkingBudget {
		// No token-budget knob in Chat Completions; use Effort instead.
		return nil, &llm.ErrUnsupportedCapability{Cap: llm.CapThinking}
	}
	h := http.Header{}
	for k, v := range p.cfg.Headers {
		h.Set(k, v)
	}
	if p.apiKey != "" {
		h.Set("Authorization", "Bearer "+p.apiKey)
	}
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
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"` // string, number or null depending on server
}

func (e apiError) code() string {
	var s string
	if json.Unmarshal(e.Code, &s) == nil && s != "" {
		return s
	}
	return e.Type
}

func parseError(status int, h http.Header, body []byte) error {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	var e apiError
	if json.Unmarshal(body, &env) == nil && len(env.Error) > 0 {
		if json.Unmarshal(env.Error, &e) != nil {
			json.Unmarshal(env.Error, &e.Message) // {"error": "text"}
		}
	}
	code := e.code()
	if status == http.StatusTooManyRequests && code == "insufficient_quota" {
		// Out of credit is not transient; keep it out of the retry path.
		return &llm.ErrProvider{Status: status, Code: code}
	}
	ctxLen := code == "context_length_exceeded" ||
		(status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge) && wire.MentionsContextLength(e.Message)
	return wire.StatusError(status, h, code, ctxLen)
}

// streamError classifies an error object sent inside an otherwise
// successful stream.
func streamError(e apiError) error {
	code := e.code()
	switch {
	case code == "context_length_exceeded" || wire.MentionsContextLength(e.Message):
		return llm.ErrContextLength
	case code == "rate_limit_exceeded" || code == "rate_limit_error" || code == "server_overloaded":
		return &llm.ErrRateLimited{}
	case code == "invalid_api_key" || code == "authentication_error":
		return llm.ErrAuth
	}
	return &llm.ErrProvider{Code: wire.SanitizeCode(code)}
}
