-- Conversation history (FR-DATA-001). Binary blobs (screenshots) live on disk;
-- rows carry only blob_path, and are always deleted before their files.
CREATE TABLE conversations (
    id               TEXT PRIMARY KEY,
    title            TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    -- Screenshots are not kept unless the user opts in per conversation (FR-SCREEN-006).
    keep_screenshots INTEGER NOT NULL DEFAULT 0 CHECK (keep_screenshots IN (0, 1)),
    agent_id         TEXT
);
CREATE INDEX conversations_updated ON conversations(updated_at DESC, id);

CREATE TABLE messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL DEFAULT '',
    generation      INTEGER NOT NULL DEFAULT 0,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'system')),
    text            TEXT NOT NULL DEFAULT '',
    mode            TEXT NOT NULL DEFAULT 'text' CHECK (mode IN ('talk', 'text', 'dictation')),
    created_at      TEXT NOT NULL,
    provider_id     TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    usage           TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(usage))
);
CREATE INDEX messages_conv ON messages(conversation_id, created_at);

CREATE TABLE captures (
    id               TEXT NOT NULL,
    message_id       TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    display_id       TEXT NOT NULL DEFAULT '',
    bounds           TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(bounds)),
    native_w         INTEGER NOT NULL DEFAULT 0,
    native_h         INTEGER NOT NULL DEFAULT 0,
    model_w          INTEGER NOT NULL DEFAULT 0,
    model_h          INTEGER NOT NULL DEFAULT 0,
    scale            REAL NOT NULL DEFAULT 1,
    rotation         INTEGER NOT NULL DEFAULT 0,
    topology_version INTEGER NOT NULL DEFAULT 0,
    taken_at         TEXT NOT NULL,
    blob_path        TEXT,
    -- Capture ids are only unique per process lifetime, so scope them by message.
    PRIMARY KEY (message_id, id)
);

CREATE TABLE attachments (
    id         TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('file', 'image', 'url')),
    name       TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT '',
    size       INTEGER NOT NULL DEFAULT 0,
    blob_path  TEXT,
    source_url TEXT
);
CREATE INDEX attachments_msg ON attachments(message_id);

-- External-content FTS5 index; unicode61 folds case for Cyrillic too.
CREATE VIRTUAL TABLE messages_fts USING fts5(
    text, content='messages', content_rowid='rowid', tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;
CREATE TRIGGER messages_au AFTER UPDATE OF text ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
    INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;
