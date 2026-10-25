package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/internal/wire"
)

func init() { llm.RegisterLister(llm.KindOpenAI, ListModels) }

type modelEntry struct {
	ID string `json:"id"`
}

// ListModels reads GET {BaseURL}/models, which OpenAI, the aggregators
// and the local servers (Ollama, LM Studio, vLLM) all answer. Prefer
// llm.ListModels, which validates the config and hardens the client.
func ListModels(ctx context.Context, cfg llm.ProviderConfig, apiKey string, client *http.Client) ([]string, error) {
	u, err := cfg.Endpoint()
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	for k, v := range cfg.Headers {
		h.Set(k, v)
	}
	if apiKey != "" {
		h.Set("Authorization", "Bearer "+apiKey)
	}
	var raw json.RawMessage
	if err := wire.GetJSON(ctx, wire.Get{Client: client, URL: u.JoinPath("models").String(), Header: h,
		Timeout: cfg.Timeout, ParseError: parseError}, &raw); err != nil {
		return nil, err
	}
	return parseModels(raw)
}

// parseModels reads {"data": [{"id": ...}]}; a few servers send the bare
// array instead.
func parseModels(raw json.RawMessage) ([]string, error) {
	var entries []modelEntry
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
		if json.Unmarshal(t, &entries) != nil {
			return nil, wire.Malformed("bad model list")
		}
	} else {
		var env struct {
			Data *[]modelEntry `json:"data"`
		}
		if json.Unmarshal(t, &env) != nil || env.Data == nil {
			return nil, wire.Malformed("bad model list")
		}
		entries = *env.Data
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids, nil
}
