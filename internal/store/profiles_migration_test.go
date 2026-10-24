package store

import (
	"context"
	"testing"
)

func TestMigration0003AppliesOnExisting(t *testing.T) {
	ctx := context.Background()
	path := tmpDB(t)
	files := map[string]string{}
	for _, m := range mustLoadBuiltin(t) {
		if m.version <= 2 {
			files[m.name] = m.sql
		}
	}
	db, err := openWith(ctx, path, memSrc(files))
	if err != nil {
		t.Fatal(err)
	}
	// Pre-existing data must survive the upgrade.
	if _, err := db.ExecContext(ctx, `INSERT INTO providers(id, name, kind, base_url, model, created_at, updated_at)
		VALUES ('p1', 'n', 'openai-compatible', 'https://x', 'm', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("0003 on top of 0001+0002: %v", err)
	}
	defer db.Close()
	for _, tbl := range []string{"model_profiles", "model_roles"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM providers`).Scan(&n); err != nil || n != 1 {
		t.Errorf("provider rows = %d, %v", n, err)
	}
}
