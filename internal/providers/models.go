package providers

import (
	"context"
	"errors"
	"strings"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/store"
)

// ModelQuery is the endpoint a settings form wants the models of, as
// typed so far: no model is needed to list them.
type ModelQuery struct {
	Kind    llm.Kind
	BaseURL string
	Headers map[string]string
	// APIKey is the key typed in the form. "" uses the key stored for
	// ProviderID, but only while the form still points at that provider's
	// host: a stored key never goes to an address it was not saved for.
	APIKey     string
	ProviderID string
}

// ListModels returns the models the queried endpoint offers (see
// llm.ListModels for order and errors). Nothing is stored.
func (s *Service) ListModels(ctx context.Context, q ModelQuery) ([]string, error) {
	cfg := llm.ProviderConfig{Kind: q.Kind, BaseURL: q.BaseURL, Headers: q.Headers}
	key := q.APIKey
	if key == "" && q.ProviderID != "" {
		rec, err := s.repo.Get(ctx, q.ProviderID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, err
		case sameHost(s.config(rec), cfg):
			if key, err = s.key(ctx, rec.SecretRef); err != nil {
				return nil, err
			}
		}
	}
	return llm.ListModels(ctx, cfg, key, s.client)
}

// sameHost reports whether a and b are the same protocol at the same
// scheme and host; the path may differ (".../v1" or not).
func sameHost(a, b llm.ProviderConfig) bool {
	if a.Kind != b.Kind {
		return false
	}
	ua, err := a.Endpoint()
	if err != nil {
		return false
	}
	ub, err := b.Endpoint()
	return err == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host)
}
