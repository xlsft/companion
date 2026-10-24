// Package profiles assigns model profiles to roles (OC-25).
//
// A profile pins exactly one provider (and optionally a model). Resolve
// returns precisely that and never anything else: if the provider lacks a
// capability the role needs, the result is llm.ErrUnsupportedCapability,
// not a different provider (FR-BYOK-006, PRD §9.4).
package profiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xlsft/openclick/internal/llm"
	"github.com/xlsft/openclick/internal/providers"
	"github.com/xlsft/openclick/internal/store"
)

// Role is a place in the product that needs a model.
type Role string

const (
	RoleTalk             Role = "talk"
	RoleVision           Role = "vision"
	RoleAgent            Role = "agent"
	RoleDictationCleanup Role = "dictation_cleanup"
)

// Roles lists every role.
var Roles = []Role{RoleTalk, RoleVision, RoleAgent, RoleDictationCleanup}

// Conventional ids of the named speed/quality profiles; a role may point at
// them like at any other profile.
const (
	ProfileFast = "fast"
	ProfileDeep = "deep"
)

// Needs returns the capabilities a role cannot work without.
func (r Role) Needs() []llm.Capability {
	switch r {
	case RoleVision:
		return []llm.Capability{llm.CapVision}
	case RoleAgent:
		return []llm.Capability{llm.CapTools}
	}
	return nil
}

func (r Role) valid() bool {
	for _, k := range Roles {
		if k == r {
			return true
		}
	}
	return false
}

// Profile is a named choice of provider, model and generation policy.
type Profile struct {
	ID         string
	Name       string
	ProviderID string
	// ModelOverride replaces the provider's model; empty means the provider's.
	ModelOverride string
	Policy        llm.ModelPolicy
}

var (
	// ErrNotAssigned: the role has no profile (never set, or its provider
	// or profile was deleted).
	ErrNotAssigned = errors.New("profiles: role not assigned")
	// ErrInvalid wraps validation failures.
	ErrInvalid = errors.New("profiles: invalid")
)

// Manager stores profiles and resolves roles.
type Manager struct {
	db        store.Querier
	providers *providers.Service
}

func New(db store.Querier, p *providers.Service) *Manager {
	return &Manager{db: db, providers: p}
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Save creates or replaces a profile. Requests already in flight keep the
// provider and model they were resolved with; only later Resolve calls see
// the change.
func (m *Manager) Save(ctx context.Context, p Profile) error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Name) == "" || p.ProviderID == "" {
		return fmt.Errorf("%w: id, name and provider are required", ErrInvalid)
	}
	if _, err := m.providers.Get(ctx, p.ProviderID); err != nil {
		return err
	}
	policy, err := json.Marshal(p.Policy)
	if err != nil {
		return err
	}
	ts := now()
	_, err = m.db.ExecContext(ctx, `INSERT INTO model_profiles(id, name, provider_id, model_override, policy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, provider_id=excluded.provider_id,
			model_override=excluded.model_override, policy=excluded.policy, updated_at=excluded.updated_at`,
		p.ID, p.Name, p.ProviderID, p.ModelOverride, string(policy), ts, ts)
	if err != nil {
		return fmt.Errorf("profiles: save: %w", err)
	}
	return nil
}

const cols = `id, name, provider_id, model_override, policy`

func scan(s interface{ Scan(...any) error }) (Profile, error) {
	var p Profile
	var policy string
	if err := s.Scan(&p.ID, &p.Name, &p.ProviderID, &p.ModelOverride, &policy); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(policy), &p.Policy); err != nil {
		return p, fmt.Errorf("profiles: decode policy: %w", err)
	}
	return p, nil
}

func (m *Manager) Get(ctx context.Context, id string) (Profile, error) {
	p, err := scan(m.db.QueryRowContext(ctx, `SELECT `+cols+` FROM model_profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, store.ErrNotFound
	}
	return p, err
}

func (m *Manager) List(ctx context.Context) ([]Profile, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+cols+` FROM model_profiles ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("profiles: list: %w", err)
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a profile; roles pointing at it become unassigned.
func (m *Manager) Delete(ctx context.Context, id string) error {
	res, err := m.db.ExecContext(ctx, `DELETE FROM model_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("profiles: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Assign points role at profile id.
func (m *Manager) Assign(ctx context.Context, role Role, profileID string) error {
	if !role.valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
	}
	if _, err := m.Get(ctx, profileID); err != nil {
		return err
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO model_roles(role, profile_id, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(role) DO UPDATE SET profile_id=excluded.profile_id, updated_at=excluded.updated_at`,
		string(role), profileID, now())
	if err != nil {
		return fmt.Errorf("profiles: assign: %w", err)
	}
	return nil
}

// Unassign clears a role.
func (m *Manager) Unassign(ctx context.Context, role Role) error {
	if _, err := m.db.ExecContext(ctx, `DELETE FROM model_roles WHERE role = ?`, string(role)); err != nil {
		return fmt.Errorf("profiles: unassign: %w", err)
	}
	return nil
}

// Assignments returns role to profile id for every assigned role.
func (m *Manager) Assignments(ctx context.Context) (map[Role]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT role, profile_id FROM model_roles`)
	if err != nil {
		return nil, fmt.Errorf("profiles: assignments: %w", err)
	}
	defer rows.Close()
	out := map[Role]string{}
	for rows.Next() {
		var r, id string
		if err := rows.Scan(&r, &id); err != nil {
			return nil, err
		}
		out[Role(r)] = id
	}
	return out, rows.Err()
}

// Resolve returns the provider and profile assigned to role: exactly those,
// with the profile's model override applied. It fails with ErrNotAssigned
// when nothing is assigned and with *llm.ErrUnsupportedCapability when the
// provider (as probed and configured) lacks a capability the role needs.
// There is no fallback to another provider or model.
func (m *Manager) Resolve(ctx context.Context, role Role) (llm.Provider, Profile, error) {
	if !role.valid() {
		return nil, Profile{}, fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
	}
	var id string
	err := m.db.QueryRowContext(ctx, `SELECT profile_id FROM model_roles WHERE role = ?`, string(role)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Profile{}, fmt.Errorf("%w: %s", ErrNotAssigned, role)
	}
	if err != nil {
		return nil, Profile{}, fmt.Errorf("profiles: resolve: %w", err)
	}
	prof, err := m.Get(ctx, id)
	if err != nil {
		return nil, Profile{}, err
	}
	p, _, err := m.providers.BuildModel(ctx, prof.ProviderID, prof.ModelOverride)
	if err != nil {
		return nil, prof, err
	}
	caps, err := m.providers.Capabilities(ctx, prof.ProviderID, p.Capabilities())
	if err != nil {
		return nil, prof, err
	}
	for _, c := range role.Needs() {
		if !caps.Has(c) {
			return nil, prof, &llm.ErrUnsupportedCapability{Cap: c}
		}
	}
	return p, prof, nil
}
