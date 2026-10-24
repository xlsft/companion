package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/store"
)

// Scope says whom a fact applies to. Only global is written today; a
// per-agent scope is reserved so agents can get their own memory later.
type Scope string

const ScopeGlobal Scope = "global"

// AgentScope is the scope of one agent's own facts.
func AgentScope(id string) Scope { return Scope("agent:" + id) }

// Kind classifies a fact for display and for slot handling.
type Kind string

const (
	KindName       Kind = "name"
	KindPreference Kind = "preference"
	KindLanguage   Kind = "language"
	KindApp        Kind = "app"
	KindOther      Kind = "other"
)

func kindOf(s string) Kind {
	switch k := Kind(strings.ToLower(strings.TrimSpace(s))); k {
	case KindName, KindPreference, KindLanguage, KindApp:
		return k
	}
	return KindOther
}

// Fact is one remembered statement about the user.
type Fact struct {
	ID    string
	Scope Scope
	Kind  Kind
	// Slot, when set, is a single-valued attribute ("name", "language"): a
	// new value replaces the old one instead of being added.
	Slot string
	Text string
	// SourceTurn refers to the turn the fact came from; empty when
	// transcripts are not saved, since it would point at nothing.
	SourceTurn string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Volatile facts live in memory for the current session only.
	Volatile bool
}

// maxFactsPerScope bounds the profile; the least recently touched fact goes
// first.
const maxFactsPerScope = 200

// Repo persists the profile.
type Repo struct {
	db  *store.DB
	now func() time.Time
}

// NewRepo returns the profile store over db.
func NewRepo(db *store.DB) *Repo { return &Repo{db: db, now: time.Now} }

func normalize(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Upsert stores f and reports the stored fact and whether anything changed.
// The same text in the same scope only refreshes its timestamp; a new value
// for an occupied slot replaces the old one.
func (r *Repo) Upsert(ctx context.Context, f Fact) (Fact, bool, error) {
	if f.Scope == "" {
		f.Scope = ScopeGlobal
	}
	norm := normalize(f.Text)
	now := r.now()
	var out Fact
	changed := false
	err := r.db.Tx(ctx, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM memory_facts WHERE scope = ? AND norm = ?`, string(f.Scope), norm).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx, `UPDATE memory_facts SET updated_at = ? WHERE id = ?`, stamp(now), id); err != nil {
				return err
			}
			out, err = get(ctx, tx, id)
			return err
		case err != sql.ErrNoRows:
			return err
		}
		changed = true
		if f.Slot != "" {
			err := tx.QueryRowContext(ctx, `SELECT id FROM memory_facts WHERE scope = ? AND slot = ? ORDER BY updated_at DESC LIMIT 1`,
				string(f.Scope), f.Slot).Scan(&id)
			if err == nil {
				if _, err := tx.ExecContext(ctx, `UPDATE memory_facts SET text = ?, norm = ?, kind = ?, source_turn = ?, updated_at = ? WHERE id = ?`,
					f.Text, norm, string(f.Kind), f.SourceTurn, stamp(now), id); err != nil {
					return err
				}
				// Older values of the same slot are stale now.
				if _, err := tx.ExecContext(ctx, `DELETE FROM memory_facts WHERE scope = ? AND slot = ? AND id <> ?`, string(f.Scope), f.Slot, id); err != nil {
					return err
				}
				out, err = get(ctx, tx, id)
				return err
			} else if err != sql.ErrNoRows {
				return err
			}
		}
		id = newID()
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_facts(id, scope, kind, slot, text, norm, source_turn, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, string(f.Scope), string(f.Kind), f.Slot, f.Text, norm, f.SourceTurn, stamp(now), stamp(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_facts WHERE scope = ? AND id NOT IN
			(SELECT id FROM memory_facts WHERE scope = ? ORDER BY updated_at DESC, id LIMIT ?)`,
			string(f.Scope), string(f.Scope), maxFactsPerScope); err != nil {
			return err
		}
		out, err = get(ctx, tx, id)
		return err
	})
	if err != nil {
		return Fact{}, false, fmt.Errorf("memory: store fact: %w", err)
	}
	return out, changed, nil
}

func get(ctx context.Context, q store.Querier, id string) (Fact, error) {
	rows, err := q.QueryContext(ctx, factSelect+` WHERE id = ?`, id)
	if err != nil {
		return Fact{}, err
	}
	fs, err := scanFacts(rows)
	if err != nil {
		return Fact{}, err
	}
	if len(fs) == 0 {
		return Fact{}, store.ErrNotFound
	}
	return fs[0], nil
}

const factSelect = `SELECT id, scope, kind, slot, text, source_turn, created_at, updated_at FROM memory_facts`

func scanFacts(rows *sql.Rows) ([]Fact, error) {
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var scope, kind, created, updated string
		if err := rows.Scan(&f.ID, &scope, &kind, &f.Slot, &f.Text, &f.SourceTurn, &created, &updated); err != nil {
			return nil, err
		}
		f.Scope, f.Kind = Scope(scope), Kind(kind)
		f.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		f.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

// List returns the facts of the given scopes, most recently updated first;
// no scopes means all.
func (r *Repo) List(ctx context.Context, scopes ...Scope) ([]Fact, error) {
	q, args := factSelect, []any{}
	if len(scopes) > 0 {
		marks := make([]string, len(scopes))
		for i, s := range scopes {
			marks[i] = "?"
			args = append(args, string(s))
		}
		q += ` WHERE scope IN (` + strings.Join(marks, ",") + `)`
	}
	rows, err := r.db.QueryContext(ctx, q+` ORDER BY updated_at DESC, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: list: %w", err)
	}
	fs, err := scanFacts(rows)
	if err != nil {
		return nil, fmt.Errorf("memory: list: %w", err)
	}
	return fs, nil
}

// Delete removes one fact; a missing id is not an error.
func (r *Repo) Delete(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM memory_facts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("memory: delete: %w", err)
	}
	return nil
}

// DeleteAll empties the profile.
func (r *Repo) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM memory_facts`); err != nil {
		return fmt.Errorf("memory: delete all: %w", err)
	}
	return nil
}
