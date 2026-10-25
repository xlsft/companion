package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
)

// maxModels caps a model list; see llm.MaxModels.
const maxModels = 1000

// ListModels reads GET {base}/models, which OpenAI and the compatible
// speech servers (Kokoro-FastAPI, Speaches) answer, for the user to pick
// the model from. The ids come back sorted as the server names them,
// without filtering: a server's own naming says best what it speaks with.
func (o *OpenAI) ListModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.cfg.timeout())
	defer cancel()
	r, err := http.NewRequest(http.MethodGet, o.cfg.BaseURL+"/models", nil)
	if err != nil {
		return nil, &Error{Kind: ErrBadRequest, Provider: o.name, Cause: stripURL(err)}
	}
	r.Header.Set("Accept", "application/json")
	if o.cfg.APIKey != "" {
		r.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	}
	resp, err := o.do(ctx, cancel, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxVoicesBody))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: ErrUnavailable, Provider: o.name, Cause: stripURL(err)}
	}
	ids, err := parseModelIDs(raw)
	if err != nil {
		return nil, &Error{Kind: ErrUnavailable, Provider: o.name, Cause: err}
	}
	return ids, nil
}

// parseModelIDs reads {"data": [{"id": ...}]} or the bare array a few
// servers send.
func parseModelIDs(raw []byte) ([]string, error) {
	type entry struct {
		ID string `json:"id"`
	}
	var entries []entry
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
		if json.Unmarshal(t, &entries) != nil {
			return nil, errors.New("bad model list")
		}
	} else {
		var env struct {
			Data *[]entry `json:"data"`
		}
		if json.Unmarshal(t, &env) != nil || env.Data == nil {
			return nil, errors.New("bad model list")
		}
		entries = *env.Data
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if id := strings.TrimSpace(e.ID); id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > maxModels {
		ids = ids[:maxModels]
	}
	return ids, nil
}
