package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/xlsft/companion/pkg/contracts"
)

func tmpDB(t *testing.T) string { return filepath.Join(t.TempDir(), "oc.db") }

func memSrc(files map[string]string) migrationSource {
	m := fstest.MapFS{}
	for n, c := range files {
		m["migrations/"+n] = &fstest.MapFile{Data: []byte(c)}
	}
	return migrationSource{fsys: m, dir: "migrations"}
}

func TestOpenMigratesFromEmpty(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"settings", "providers", "platform_capabilities", "schema_migrations"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
	var mode string
	db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q", mode)
	}
	var fk int
	db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Errorf("foreign_keys = %d", fk)
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := tmpDB(t)
	for i := 0; i < 3; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n)
		if want := len(mustLoadBuiltin(t)); n != want {
			t.Errorf("open #%d: %d migration rows, want %d", i, n, want)
		}
		db.Close()
	}
}

func TestFailingMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	path := tmpDB(t)
	src := memSrc(map[string]string{
		"0001_ok.sql":  `CREATE TABLE a (x INTEGER);`,
		"0002_bad.sql": `CREATE TABLE b (x INTEGER); INSERT INTO nope VALUES (1);`,
	})
	if _, err := openWith(ctx, path, src); err == nil {
		t.Fatal("expected migration error")
	}
	// The good one is kept, the bad one left nothing behind.
	db, err := openWith(ctx, path, memSrc(map[string]string{"0001_ok.sql": `CREATE TABLE a (x INTEGER);`}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'b'`).Scan(&n)
	if n != 0 {
		t.Error("table b survived failed migration")
	}
	var v int
	db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v)
	if v != 1 {
		t.Errorf("version = %d, want 1", v)
	}
}

func TestDestructiveMigrationBacksUp(t *testing.T) {
	ctx := context.Background()
	path := tmpDB(t)
	db, err := openWith(ctx, path, memSrc(map[string]string{
		"0001_a.sql": `CREATE TABLE a (x INTEGER); INSERT INTO a VALUES (42);`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if m, _ := filepath.Glob(path + ".bak-*"); len(m) != 0 {
		t.Fatalf("unexpected backups: %v", m)
	}

	db, err = openWith(ctx, path, memSrc(map[string]string{
		"0001_a.sql": `CREATE TABLE a (x INTEGER); INSERT INTO a VALUES (42);`,
		"0002_d.sql": "-- destructive\nDROP TABLE a;",
	}))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	m, _ := filepath.Glob(path + ".bak-0002-*")
	if len(m) != 1 {
		t.Fatalf("backups = %v", m)
	}
	// Backup is a usable DB that still has the dropped data.
	bak, err := sql.Open("sqlite", m[0])
	if err != nil {
		t.Fatal(err)
	}
	defer bak.Close()
	var x int
	if err := bak.QueryRowContext(ctx, `SELECT x FROM a`).Scan(&x); err != nil || x != 42 {
		t.Errorf("backup content x=%d err=%v", x, err)
	}
}

func TestTxRollback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, tmpDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	kv := NewKV(db)

	boom := errors.New("boom")
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		if err := NewKV(tx).Set(ctx, "k", []byte(`1`)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := kv.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("value survived rollback: %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		_ = db.Tx(ctx, func(tx *sql.Tx) error {
			_ = NewKV(tx).Set(ctx, "p", []byte(`1`))
			panic("x")
		})
	}()
	if _, err := kv.Get(ctx, "p"); !errors.Is(err, ErrNotFound) {
		t.Errorf("value survived panic: %v", err)
	}

	if err := db.Tx(ctx, func(tx *sql.Tx) error { return NewKV(tx).Set(ctx, "c", []byte(`2`)) }); err != nil {
		t.Fatal(err)
	}
	if v, err := kv.Get(ctx, "c"); err != nil || string(v) != "2" {
		t.Errorf("commit lost: %s %v", v, err)
	}
}

func TestKVJSON(t *testing.T) {
	ctx := context.Background()
	db, _ := Open(ctx, tmpDB(t))
	defer db.Close()
	kv := NewKV(db)
	type v struct{ A int }
	if err := SetJSON(ctx, kv, "x", v{A: 3}); err != nil {
		t.Fatal(err)
	}
	if err := SetJSON(ctx, kv, "x", v{A: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := GetJSON[v](ctx, kv, "x")
	if err != nil || got.A != 4 {
		t.Errorf("got %+v %v", got, err)
	}
	if err := kv.Delete(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetJSON[v](ctx, kv, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestProviderRepo(t *testing.T) {
	ctx := context.Background()
	db, _ := Open(ctx, tmpDB(t))
	defer db.Close()
	r := NewProviderRepo(db)

	p := &Provider{ID: "p1", Name: "main", Kind: KindOpenAI, BaseURL: "http://x", Model: "m",
		Headers: map[string]string{"X-A": "b"}, TimeoutMS: 5000, SecretRef: contracts.SecretRef("provider/p1/api_key")}
	if err := r.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["X-A"] != "b" || got.SecretRef != p.SecretRef || got.TimeoutMS != 5000 || got.CreatedAt.IsZero() {
		t.Errorf("got %+v", got)
	}

	got.Model = "m2"
	got.Kind = KindAnthropic
	if err := r.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, _ := r.List(ctx)
	if len(list) != 1 || list[0].Model != "m2" || list[0].Kind != KindAnthropic {
		t.Errorf("list %+v", list)
	}

	bad := &Provider{ID: "p2", Name: "x", Kind: "weird", BaseURL: "u", Model: "m"}
	if err := r.Create(ctx, bad); err == nil {
		t.Error("kind check not enforced")
	}
	if err := r.Update(ctx, &Provider{ID: "nope", Kind: KindOpenAI}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	if err := r.Delete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := r.Delete(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}
