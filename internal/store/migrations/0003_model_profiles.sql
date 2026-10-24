-- Model profiles (OC-25, FR-BYOK-005/006): a profile pins one provider and
-- optionally a model plus generation policy; roles point at profiles.
-- Cascades mean deleting a provider leaves a role unassigned (an explicit
-- "not configured" error) rather than silently pointing somewhere else.
CREATE TABLE model_profiles (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    provider_id    TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    model_override TEXT NOT NULL DEFAULT '',
    policy         TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(policy)),
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
CREATE INDEX model_profiles_provider ON model_profiles(provider_id);

CREATE TABLE model_roles (
    role       TEXT PRIMARY KEY CHECK (role IN ('talk', 'vision', 'agent', 'dictation_cleanup')),
    profile_id TEXT NOT NULL REFERENCES model_profiles(id) ON DELETE CASCADE,
    updated_at TEXT NOT NULL
);
