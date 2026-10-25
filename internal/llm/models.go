package llm

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// MaxModels caps a model list. Aggregators offer a few hundred models; a
// server answering with far more is broken or hostile, and a picker of
// thousands is unusable anyway.
const MaxModels = 1000

// Lister fetches the model ids an endpoint offers. client is already
// hardened by ListModels.
type Lister func(ctx context.Context, cfg ProviderConfig, apiKey string, client *http.Client) ([]string, error)

var listers = map[Kind]Lister{}

// RegisterLister makes an adapter's model listing available to
// ListModels; adapter packages call it from init, like Register.
func RegisterLister(kind Kind, f Lister) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := listers[kind]; dup {
		panic("llm: model lister registered twice for " + string(kind))
	}
	listers[kind] = f
}

// ListModels returns the model ids the endpoint of cfg offers, sorted and
// without duplicates, at most MaxModels. cfg.Name and cfg.Model may be
// empty: the list is what the user picks the model from. Failures are the
// normalized errors of Stream (ErrAuth, ErrNetwork, *ErrProvider for e.g.
// a 404 from a server without a listing, ...). The caller bounds the call
// with ctx; cfg.Timeout, if set, bounds it too.
func ListModels(ctx context.Context, cfg ProviderConfig, apiKey string, client *http.Client) ([]string, error) {
	if err := cfg.validateConnection(); err != nil {
		return nil, err
	}
	hc, err := hardenClient(client)
	if err != nil {
		return nil, err
	}
	registryMu.RLock()
	f := listers[cfg.Kind]
	registryMu.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoAdapter, cfg.Kind)
	}
	ids, err := f(ctx, cfg, apiKey, hc)
	if err != nil {
		return nil, err
	}
	return normalizeModels(ids), nil
}

// normalizeModels trims, dedupes and sorts ids case-insensitively (ties
// by byte order, so the order is stable), keeping the first MaxModels.
func normalizeModels(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return cmp.Or(strings.Compare(strings.ToLower(a), strings.ToLower(b)), strings.Compare(a, b))
	})
	out = slices.Compact(out)
	if len(out) > MaxModels {
		out = out[:MaxModels]
	}
	return out
}
