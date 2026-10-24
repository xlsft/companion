package main

import (
	"context"

	"github.com/xlsft/companion/internal/readiness"
	"github.com/xlsft/companion/internal/store"
)

// openGate loads the persisted setup progress from the settings KV.
func openGate(ctx context.Context, kv *store.KV, checks map[readiness.Step]readiness.Check) (*readiness.Gate, error) {
	return readiness.Open(ctx, readiness.KVStore{KV: kv}, checks)
}
