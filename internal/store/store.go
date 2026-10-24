// Package store owns the SQLite database: opening, migrations, transactions.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the core cgo-free (ADR-0001)
)

// Querier is satisfied by *sql.DB and *sql.Tx so repositories work in both.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is the opened, migrated database.
type DB struct {
	*sql.DB
	path string
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	return openWith(ctx, path, builtinMigrations)
}

func openWith(ctx context.Context, path string, src migrationSource) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}
	q := url.Values{}
	// WAL for concurrent readers, FK enforcement, bounded wait on locks, and
	// immediate write transactions so lock upgrades don't fail with SQLITE_BUSY.
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_txlock", "immediate")
	p := filepath.ToSlash(abs)
	if len(p) > 1 && p[1] == ':' { // windows drive letter needs a leading slash in file: URIs
		p = "/" + p
	}
	dsn := (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String()

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db := &DB{DB: sqlDB, path: abs}
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := db.migrate(ctx, src); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Path returns the absolute database file path.
func (d *DB) Path() string { return d.path }

// Tx runs fn in a transaction, rolling back on error or panic.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
