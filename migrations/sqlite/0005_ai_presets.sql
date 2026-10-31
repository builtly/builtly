-- SQLite 0005_ai_presets.sql

CREATE TABLE connections_new (
    id           TEXT PRIMARY KEY,
    provider     TEXT NOT NULL CHECK (provider IN (
                     'stripe', 'resend', 'r2', 'google_oauth', 'github_oauth',
                     'apple_signin', 'apple_iap', 'google_play', 'posthog', 'promotekit', 'affonso',
                     'openai_compatible')),
    name         TEXT NOT NULL,
    settings     TEXT NOT NULL DEFAULT '{}',
    secrets_enc  BLOB NOT NULL,
    key_version  INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

INSERT INTO connections_new SELECT * FROM connections;
DROP TABLE connections;
ALTER TABLE connections_new RENAME TO connections;
CREATE INDEX idx_connections_provider ON connections (provider);

CREATE TABLE ai_presets (
    id                    TEXT PRIMARY KEY,
    app_id                TEXT NOT NULL REFERENCES apps(id),
    slug                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL,
    system_prompt         TEXT NOT NULL DEFAULT '',
    user_prompt_template  TEXT NOT NULL DEFAULT '',
    temperature           REAL NOT NULL DEFAULT 0.7,
    max_tokens            INTEGER NOT NULL DEFAULT 2048,
    response_format       TEXT NOT NULL DEFAULT 'text',
    credits_cost          INTEGER NOT NULL DEFAULT 1 CHECK (credits_cost >= 0),
    published             INTEGER NOT NULL DEFAULT 1,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    CONSTRAINT uq_ai_presets_app_slug UNIQUE (app_id, slug)
);
CREATE INDEX idx_ai_presets_app ON ai_presets (app_id, created_at DESC);
