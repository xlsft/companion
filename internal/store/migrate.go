package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migrationSource struct {
	fsys fs.FS
	dir  string
}

var builtinMigrations = migrationSource{fsys: migrationsFS, dir: "migrations"}

var migrationName = regexp.MustCompile(`^(\d{4})_[A-Za-z0-9_]+\.sql$`)

// destructiveMarker on the first line asks for a backup before applying.
const destructiveMarker = "-- destructive"

type migration struct {
	version     int
	name        string
	sql         string
	destructive bool
}

func (s migrationSource) load() ([]migration, error) {
	entries, err := fs.ReadDir(s.fsys, s.dir)
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("store: duplicate migration version %d (%s, %s)", v, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(s.fsys, s.dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		text := string(b)
		first, _, _ := strings.Cut(text, "\n")
		out = append(out, migration{
			version:     v,
			name:        e.Name(),
			sql:         text,
			destructive: strings.EqualFold(strings.TrimSpace(first), destructiveMarker),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func (d *DB) migrate(ctx context.Context, src migrationSource) error {
	migs, err := src.load()
	if err != nil {
		return err
	}
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var current int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if m.destructive {
			if err := d.backup(ctx, m.version); err != nil {
				return err
			}
		}
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
				m.version, time.Now().UTC().Format(time.RFC3339Nano))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
	}
	return nil
}

// backup copies the database with VACUUM INTO, which yields a consistent
// snapshot even in WAL mode (a plain file copy could miss the -wal contents).
func (d *DB) backup(ctx context.Context, version int) error {
	dst := fmt.Sprintf("%s.bak-%04d-%s", d.path, version, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("store: backup target exists: %s", filepath.Base(dst))
	}
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("store: backup before migration %d: %w", version, err)
	}
	return nil
}
