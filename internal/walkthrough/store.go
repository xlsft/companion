package walkthrough

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xlsft/companion/internal/store"
)

// Store keeps the progress of the walkthrough in progress; there is at
// most one.
type Store interface {
	// Load returns the stored walkthrough; ok is false when there is none.
	Load(ctx context.Context) (st State, ok bool, err error)
	Save(ctx context.Context, st State) error
	Delete(ctx context.Context) error
}

// KV is the part of *store.KV the walkthrough is kept in.
type KV interface {
	Get(ctx context.Context, key string) (json.RawMessage, error)
	Set(ctx context.Context, key string, value json.RawMessage) error
	Delete(ctx context.Context, key string) error
}

// kvKey is where the walkthrough lives in the key-value table.
const kvKey = "walkthrough.current"

type kvStore struct{ kv KV }

// NewKVStore keeps the walkthrough under one key of kv.
func NewKVStore(kv KV) Store { return kvStore{kv: kv} }

func (s kvStore) Load(ctx context.Context) (State, bool, error) {
	raw, err := s.kv.Get(ctx, kvKey)
	if errors.Is(err, store.ErrNotFound) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, false, fmt.Errorf("walkthrough: decode stored progress: %w", err)
	}
	if !st.Status.Active() || st.Plan.Validate() != nil || st.Index < 0 || st.Index >= len(st.Plan.Steps) {
		// Unusable leftovers are dropped, never shown half-broken.
		return State{}, false, nil
	}
	if len(st.Done) != len(st.Plan.Steps) {
		st.Done = nil
	}
	return st, true, nil
}

func (s kvStore) Save(ctx context.Context, st State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.kv.Set(ctx, kvKey, raw)
}

func (s kvStore) Delete(ctx context.Context) error { return s.kv.Delete(ctx, kvKey) }
