package readiness

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xlsft/openclick/internal/store"
)

func TestKVStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "oc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := KVStore{KV: store.NewKV(db)}

	data, err := st.Load(ctx)
	if err != nil || data != nil {
		t.Fatalf("empty store: %q, %v", data, err)
	}
	checks := map[Step]Check{}
	for _, s := range Steps {
		checks[s] = pass
	}
	g, err := Open(ctx, st, checks)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Steps {
		if _, err := g.Run(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// A restart over the same KV stays Ready without re-running setup.
	g2, err := Open(ctx, st, checks)
	if err != nil {
		t.Fatal(err)
	}
	if !g2.IsReady() {
		t.Errorf("reopened gate state = %s", g2.Progress().State)
	}
}
