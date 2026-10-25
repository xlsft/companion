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
	raw, err := o.models(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := parseModelIDs(raw)
	if err != nil {
		return nil, &Error{Kind: ErrUnavailable, Provider: o.name, Cause: err}
	}
	return ids, nil
}

// openRouter reports OpenRouter, whose /models lists every model it routes
// and whose speech voices differ per model.
func (o *OpenAI) openRouter() bool {
	return strings.Contains(strings.ToLower(o.cfg.BaseURL), "openrouter.ai")
}

// models fetches GET {base}/models; on OpenRouter only the speech models.
func (o *OpenAI) models(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, o.cfg.timeout())
	defer cancel()
	u := o.cfg.BaseURL + "/models"
	if o.openRouter() {
		u += "?output_modalities=speech"
	}
	r, err := http.NewRequest(http.MethodGet, u, nil)
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
	return raw, nil
}

// routerVoices reads the voices OpenRouter lists for model in the
// supported_voices field of its model entry: plain names or objects.
func routerVoices(raw []byte, model string) ([]Voice, bool) {
	var env struct {
		Data []struct {
			ID     string            `json:"id"`
			Voices []json.RawMessage `json:"supported_voices"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil, false
	}
	for _, m := range env.Data {
		if m.ID != model {
			continue
		}
		var out []Voice
		for _, v := range m.Voices {
			var name string
			if json.Unmarshal(v, &name) == nil {
				if name = strings.TrimSpace(name); name != "" {
					out = append(out, Voice{ID: name, Name: name})
				}
				continue
			}
			var obj struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				VoiceID string `json:"voice_id"`
			}
			if json.Unmarshal(v, &obj) == nil {
				id := cmpOr(obj.ID, obj.VoiceID, obj.Name)
				if id != "" {
					out = append(out, Voice{ID: id, Name: cmpOr(obj.Name, id)})
				}
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

func cmpOr(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
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
