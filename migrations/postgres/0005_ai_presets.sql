-- PostgreSQL 0005_ai_presets.sql

ALTER TABLE connections DROP CONSTRAINT IF EXISTS connections_provider_check;
ALTER TABLE connections ADD CONSTRAINT connections_provider_check CHECK (provider IN (
    'stripe', 'resend', 'r2', 'google_oauth', 'github_oauth',
    'apple_signin', 'apple_iap', 'google_play', 'posthog', 'promotekit', 'affonso',
    'openai_compatible'
));

CREATE TABLE ai_presets (
    id                    UUID PRIMARY KEY,
    app_id                UUID NOT NULL REFERENCES apps(id),
    slug                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL,
    system_prompt         TEXT NOT NULL DEFAULT '',
    user_prompt_template  TEXT NOT NULL DEFAULT '',
    temperature           DOUBLE PRECISION NOT NULL DEFAULT 0.7,
    max_tokens            INTEGER NOT NULL DEFAULT 2048,
    response_format       TEXT NOT NULL DEFAULT 'text',
    credits_cost          BIGINT NOT NULL DEFAULT 1 CHECK (credits_cost >= 0),
    published             BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_ai_presets_app_slug UNIQUE (app_id, slug)
);
CREATE INDEX idx_ai_presets_app ON ai_presets (app_id, created_at DESC);
