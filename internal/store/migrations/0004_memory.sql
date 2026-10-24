-- Persistent memory profile (OC-87): short user facts the assistant may use
-- in later answers. scope is 'global' or 'agent:<id>' so per-agent memory can
-- follow without a schema change. norm is the lowercased, whitespace-folded
-- fact, which is what dedupe compares; slot is an optional key ("name",
-- "language") whose newer value replaces the older one instead of adding to it.
-- source_turn is an opaque reference to the turn the fact came from and is
-- empty when transcripts are not saved.
CREATE TABLE memory_facts (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL DEFAULT 'global',
    kind        TEXT NOT NULL DEFAULT 'other',
    slot        TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL,
    norm        TEXT NOT NULL,
    source_turn TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX memory_facts_norm ON memory_facts(scope, norm);
CREATE INDEX memory_facts_slot ON memory_facts(scope, slot) WHERE slot <> '';
