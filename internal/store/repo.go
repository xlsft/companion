package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xlsft/companion/pkg/contracts"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// KV is a key/JSON-value view over the settings table.
type KV struct{ q Querier }

func NewKV(q Querier) *KV { return &KV{q: q} }

// Get returns the raw JSON stored under key, or ErrNotFound.
func (k *KV) Get(ctx context.Context, key string) (json.RawMessage, error) {
	var v string
	err := k.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: kv get: %w", err)
	}
	return json.RawMessage(v), nil
}

// Set stores raw JSON under key (must be valid JSON).
func (k *KV) Set(ctx context.Context, key string, value json.RawMessage) error {
	_, err := k.q.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(value), now())
	if err != nil {
		return fmt.Errorf("store: kv set: %w", err)
	}
	return nil
}

func (k *KV) Delete(ctx context.Context, key string) error {
	if _, err := k.q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: kv delete: %w", err)
	}
	return nil
}

// GetJSON decodes the value under key into T.
func GetJSON[T any](ctx context.Context, k *KV, key string) (T, error) {
	var out T
	raw, err := k.Get(ctx, key)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("store: kv decode %q: %w", key, err)
	}
	return out, nil
}

// SetJSON encodes v and stores it under key.
func SetJSON[T any](ctx context.Context, k *KV, key string, v T) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: kv encode %q: %w", key, err)
	}
	return k.Set(ctx, key, raw)
}

// Provider kinds accepted by the schema.
const (
	KindOpenAI    = "openai-compatible"
	KindAnthropic = "anthropic-compatible"
)

// Provider is a BYOK LLM endpoint. SecretRef points into the OS secret
// store; the API key is never part of this record.
type Provider struct {
	ID           string
	Name         string
	Kind         string
	BaseURL      string
	Model        string
	Headers      map[string]string
	TimeoutMS    int
	Retry        json.RawMessage
	Capabilities json.RawMessage
	SecretRef    contracts.SecretRef
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ProviderRepo does CRUD over the providers table.
type ProviderRepo struct{ q Querier }

func NewProviderRepo(q Querier) *ProviderRepo { return &ProviderRepo{q: q} }

func jsonOrEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// Create inserts p, stamping CreatedAt/UpdatedAt.
func (r *ProviderRepo) Create(ctx context.Context, p *Provider) error {
	headers, err := json.Marshal(p.Headers)
	if err != nil {
		return fmt.Errorf("store: provider headers: %w", err)
	}
	if p.Headers == nil {
		headers = []byte("{}")
	}
	ts := time.Now().UTC()
	_, err = r.q.ExecContext(ctx, `INSERT INTO providers
		(id, name, kind, base_url, model, headers, timeout_ms, retry, capabilities, secret_ref, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Kind, p.BaseURL, p.Model, string(headers), p.TimeoutMS,
		jsonOrEmpty(p.Retry), jsonOrEmpty(p.Capabilities), string(p.SecretRef),
		ts.Format(time.RFC3339Nano), ts.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: provider create: %w", err)
	}
	p.CreatedAt, p.UpdatedAt = ts, ts
	return nil
}

const providerCols = `id, name, kind, base_url, model, headers, timeout_ms, retry, capabilities, secret_ref, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanProvider(s rowScanner) (*Provider, error) {
	var (
		p                         Provider
		headers, retry, caps, ref string
		created, updated          string
	)
	if err := s.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.Model, &headers, &p.TimeoutMS,
		&retry, &caps, &ref, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(headers), &p.Headers); err != nil {
		return nil, fmt.Errorf("store: provider headers: %w", err)
	}
	p.Retry, p.Capabilities = json.RawMessage(retry), json.RawMessage(caps)
	p.SecretRef = contracts.SecretRef(ref)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &p, nil
}

func (r *ProviderRepo) Get(ctx context.Context, id string) (*Provider, error) {
	p, err := scanProvider(r.q.QueryRowContext(ctx, `SELECT `+providerCols+` FROM providers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: provider get: %w", err)
	}
	return p, nil
}

// List returns all providers ordered by creation time.
func (r *ProviderRepo) List(ctx context.Context) ([]*Provider, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+providerCols+` FROM providers ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: provider list: %w", err)
	}
	defer rows.Close()
	var out []*Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("store: provider list: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update overwrites every mutable field of the provider with p.ID.
func (r *ProviderRepo) Update(ctx context.Context, p *Provider) error {
	headers, err := json.Marshal(p.Headers)
	if err != nil {
		return fmt.Errorf("store: provider headers: %w", err)
	}
	if p.Headers == nil {
		headers = []byte("{}")
	}
	ts := time.Now().UTC()
	res, err := r.q.ExecContext(ctx, `UPDATE providers SET name=?, kind=?, base_url=?, model=?, headers=?,
		timeout_ms=?, retry=?, capabilities=?, secret_ref=?, updated_at=? WHERE id=?`,
		p.Name, p.Kind, p.BaseURL, p.Model, string(headers), p.TimeoutMS,
		jsonOrEmpty(p.Retry), jsonOrEmpty(p.Capabilities), string(p.SecretRef),
		ts.Format(time.RFC3339Nano), p.ID)
	if err != nil {
		return fmt.Errorf("store: provider update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	p.UpdatedAt = ts
	return nil
}

func (r *ProviderRepo) Delete(ctx context.Context, id string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: provider delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
