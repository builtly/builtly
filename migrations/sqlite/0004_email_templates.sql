CREATE TABLE email_templates (
    id          TEXT PRIMARY KEY,
    app_id      TEXT NOT NULL REFERENCES apps(id),
    key         TEXT NOT NULL,
    name        TEXT NOT NULL,
    subject     TEXT NOT NULL,
    html        TEXT NOT NULL,
    text        TEXT NOT NULL DEFAULT '',
    variables   TEXT NOT NULL DEFAULT '[]',
    published   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX uq_email_templates_app_key ON email_templates (app_id, key);
CREATE INDEX idx_email_templates_app ON email_templates (app_id, created_at);

CREATE TABLE email_trigger_bindings (
    app_id      TEXT NOT NULL REFERENCES apps(id),
    trigger     TEXT NOT NULL,
    template_id TEXT NOT NULL REFERENCES email_templates(id),
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (app_id, trigger)
);

CREATE TABLE email_sends (
    app_id          TEXT NOT NULL REFERENCES apps(id),
    idempotency_key TEXT NOT NULL,
    template_key    TEXT NOT NULL,
    variables_hash  TEXT NOT NULL,
    outbox_id       TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    PRIMARY KEY (app_id, idempotency_key)
);
