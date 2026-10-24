CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL CHECK (json_valid(value)),
    updated_at TEXT NOT NULL
);

CREATE TABLE providers (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('openai-compatible', 'anthropic-compatible')),
    base_url     TEXT NOT NULL,
    model        TEXT NOT NULL,
    headers      TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(headers)),
    timeout_ms   INTEGER NOT NULL DEFAULT 0,
    retry        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(retry)),
    capabilities TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(capabilities)),
    -- Reference into the OS secret store; the key itself never lands here (FR-BYOK-003).
    secret_ref   TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE platform_capabilities (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL CHECK (json_valid(value)),
    detected_at TEXT NOT NULL
);
