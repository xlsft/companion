package store

import (
	"context"
	"testing"
)

func mustLoadBuiltin(t *testing.T) []migration {
	t.Helper()
	migs, err := builtinMigrations.load()
	if err != nil {
		t.Fatal(err)
	}
	return migs
}

func TestMigration0002AppliesOn0001(t *testing.T) {
	ctx := context.Background()
	path := tmpDB(t)
	files := map[string]string{}
	for _, m := range mustLoadBuiltin(t) {
		if m.version == 1 {
			files[m.name] = m.sql
		}
	}
	db, err := openWith(ctx, path, memSrc(files))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("0002 on top of 0001: %v", err)
	}
	defer db.Close()
	for _, tbl := range []string{"conversations", "messages", "captures", "attachments", "messages_fts"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
}
