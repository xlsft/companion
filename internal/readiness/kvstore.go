package readiness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xlsft/openclick/internal/store"
)

// ProgressKey is the settings KV key holding the persisted Progress.
const ProgressKey = "readiness.progress"

// KVStore persists Progress under ProgressKey in the settings KV.
type KVStore struct{ KV *store.KV }

func (s KVStore) Load(ctx context.Context) ([]byte, error) {
	raw, err := s.KV.Get(ctx, ProgressKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return raw, err
}

func (s KVStore) Save(ctx context.Context, data []byte) error {
	return s.KV.Set(ctx, ProgressKey, json.RawMessage(data))
}
