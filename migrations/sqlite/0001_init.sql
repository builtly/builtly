-- builtly initial schema (SQLite)

CREATE TABLE apps (
    id          TEXT PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    settings    TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE connections (
    id           TEXT PRIMARY KEY,
    provider     TEXT NOT NULL CHECK (provider IN ('stripe', 'resend', 'r2', 'google_oauth', 'github_oauth',
                     'apple_signin', 'apple_iap', 'google_play', 'posthog', 'promotekit', 'affonso')),
    name         TEXT NOT NULL,
    settings     TEXT NOT NULL DEFAULT '{}',
    secrets_enc  BLOB NOT NULL,
    key_version  INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX idx_connections_provider ON connections (provider);

CREATE TABLE app_integrations (
    app_id         TEXT NOT NULL REFERENCES apps(id),
    provider       TEXT NOT NULL,
    connection_id  TEXT NULL REFERENCES connections(id),
    settings       TEXT NOT NULL DEFAULT '{}',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (app_id, provider)
);
CREATE INDEX idx_app_integrations_conn ON app_integrations (connection_id);
CREATE UNIQUE INDEX uq_app_integrations_exclusive ON app_integrations (provider, connection_id)
    WHERE provider IN ('stripe', 'apple_iap', 'google_play') AND connection_id IS NOT NULL;
CREATE UNIQUE INDEX uq_app_integrations_exclusive_default ON app_integrations (provider)
    WHERE provider IN ('stripe', 'apple_iap', 'google_play') AND connection_id IS NULL;

CREATE TABLE app_api_keys (
    id            TEXT PRIMARY KEY,
    app_id        TEXT NOT NULL REFERENCES apps(id),
    name          TEXT NOT NULL,
    prefix        TEXT NOT NULL,
    key_hash      BLOB NOT NULL UNIQUE,
    scopes        TEXT NOT NULL DEFAULT '["credits:consume"]',
    last_used_at  TEXT NULL,
    revoked_at    TEXT NULL,
    created_at    TEXT NOT NULL
);
CREATE INDEX idx_app_api_keys_app ON app_api_keys (app_id);

CREATE TABLE users (
    id                 TEXT PRIMARY KEY,
    app_id             TEXT NOT NULL REFERENCES apps(id),
    email              TEXT NULL,
    email_verified_at  TEXT NULL,
    password_hash      TEXT NULL,
    display_name       TEXT NOT NULL DEFAULT '',
    avatar_url         TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    attribution        TEXT NOT NULL DEFAULT '{}',
    referral_code      TEXT NULL,
    last_login_at      TEXT NULL,
    deleted_at         TEXT NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    CONSTRAINT uq_users_app_id UNIQUE (app_id, id)
);
CREATE UNIQUE INDEX uq_users_app_email    ON users (app_id, email)         WHERE email IS NOT NULL;
CREATE UNIQUE INDEX uq_users_app_referral ON users (app_id, referral_code) WHERE referral_code IS NOT NULL;

CREATE TABLE user_identities (
    id                      TEXT PRIMARY KEY,
    app_id                  TEXT NOT NULL,
    user_id                 TEXT NOT NULL,
    provider                TEXT NOT NULL CHECK (provider IN ('google', 'github', 'apple')),
    provider_subject        TEXT NOT NULL,
    email                   TEXT NULL,
    email_verified          INTEGER NOT NULL,
    provider_refresh_enc    BLOB NULL,
    created_at              TEXT NOT NULL,
    last_used_at            TEXT NOT NULL,
    CONSTRAINT uq_identity UNIQUE (app_id, provider, provider_subject),
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);
CREATE INDEX idx_identities_user ON user_identities (app_id, user_id);

CREATE TABLE refresh_tokens (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    family_id    TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    auth_method  TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT NULL,
    replaced_by  TEXT NULL,
    user_agent   TEXT NOT NULL DEFAULT '',
    ip           TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);
CREATE INDEX idx_refresh_user   ON refresh_tokens (app_id, user_id);
CREATE INDEX idx_refresh_family ON refresh_tokens (family_id);

CREATE TABLE email_tokens (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    purpose      TEXT NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    email        TEXT NOT NULL,
    token_hash   BLOB NOT NULL UNIQUE,
    expires_at   TEXT NOT NULL,
    consumed_at  TEXT NULL,
    created_at   TEXT NOT NULL,
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);
CREATE INDEX idx_email_tokens_user ON email_tokens (app_id, user_id, purpose);

CREATE TABLE oauth_flows (
    id                     TEXT PRIMARY KEY,
    app_id                 TEXT NOT NULL REFERENCES apps(id),
    provider               TEXT NOT NULL,
    connection_id          TEXT NOT NULL,
    state_hash             BLOB NOT NULL UNIQUE,
    redirect_uri           TEXT NOT NULL,
    client_code_challenge  TEXT NOT NULL,
    provider_verifier_enc  BLOB NOT NULL,
    nonce                  TEXT NOT NULL,
    attribution            TEXT NOT NULL DEFAULT '{}',
    referral_code          TEXT NULL,
    login_code_hash        BLOB NULL UNIQUE,
    user_id                TEXT NULL,
    status                 TEXT NOT NULL CHECK (status IN ('started', 'authenticated', 'exchanged', 'failed')),
    expires_at             TEXT NOT NULL,
    created_at             TEXT NOT NULL
);

CREATE TABLE products (
    id                 TEXT PRIMARY KEY,
    app_id             TEXT NOT NULL REFERENCES apps(id),
    sku                TEXT NOT NULL,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    kind               TEXT NOT NULL DEFAULT 'credit_pack' CHECK (kind IN ('credit_pack')),
    credits            INTEGER NOT NULL CHECK (credits > 0),
    price_amount       INTEGER NOT NULL CHECK (price_amount >= 0),
    currency           TEXT NOT NULL,
    stripe_price_id    TEXT NULL,
    apple_product_id   TEXT NULL,
    google_product_id  TEXT NULL,
    active             INTEGER NOT NULL DEFAULT 1,
    sort_order         INTEGER NOT NULL DEFAULT 0,
    metadata           TEXT NOT NULL DEFAULT '{}',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    CONSTRAINT uq_products_app_sku UNIQUE (app_id, sku),
    CONSTRAINT uq_products_app_id  UNIQUE (app_id, id)
);
CREATE UNIQUE INDEX uq_products_stripe ON products (app_id, stripe_price_id)   WHERE stripe_price_id   IS NOT NULL;
CREATE UNIQUE INDEX uq_products_apple  ON products (app_id, apple_product_id)  WHERE apple_product_id  IS NOT NULL;
CREATE UNIQUE INDEX uq_products_google ON products (app_id, google_product_id) WHERE google_product_id IS NOT NULL;

CREATE TABLE purchases (
    id                TEXT PRIMARY KEY,
    app_id            TEXT NOT NULL,
    user_id           TEXT NOT NULL,
    product_id        TEXT NOT NULL,
    provider          TEXT NOT NULL CHECK (provider IN ('stripe', 'apple', 'google')),
    connection_id     TEXT NOT NULL,
    provider_ref      TEXT NULL,
    payment_ref       TEXT NULL,
    status            TEXT NOT NULL CHECK (status IN
                          ('pending', 'completed', 'partially_refunded', 'refunded', 'failed', 'expired')),
    credits           INTEGER NOT NULL CHECK (credits > 0),
    refunded_credits  INTEGER NOT NULL DEFAULT 0 CHECK (refunded_credits >= 0 AND refunded_credits <= credits),
    amount            INTEGER NULL,
    currency          TEXT NULL,
    environment       TEXT NOT NULL CHECK (environment IN ('production', 'sandbox')),
    affiliate_ref     TEXT NULL,
    provider_data     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    completed_at      TEXT NULL,
    FOREIGN KEY (app_id, user_id)    REFERENCES users (app_id, id),
    FOREIGN KEY (app_id, product_id) REFERENCES products (app_id, id)
);
CREATE UNIQUE INDEX uq_purchases_ref ON purchases (app_id, provider, provider_ref) WHERE provider_ref IS NOT NULL;
CREATE INDEX idx_purchases_payment_ref ON purchases (provider, payment_ref) WHERE payment_ref IS NOT NULL;
CREATE INDEX idx_purchases_user ON purchases (app_id, user_id, created_at DESC);

CREATE TABLE webhook_events (
    id             TEXT PRIMARY KEY,
    provider       TEXT NOT NULL CHECK (provider IN ('stripe', 'apple')),
    connection_id  TEXT NOT NULL,
    event_id       TEXT NOT NULL,
    event_type     TEXT NOT NULL,
    app_id         TEXT NULL,
    status         TEXT NOT NULL CHECK (status IN ('received', 'processed', 'ignored', 'failed')),
    attempts       INTEGER NOT NULL DEFAULT 1,
    last_error     TEXT NOT NULL DEFAULT '',
    received_at    TEXT NOT NULL,
    processed_at   TEXT NULL,
    CONSTRAINT uq_webhook_events UNIQUE (provider, connection_id, event_id)
);

CREATE TABLE credit_balances (
    app_id      TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    balance     INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (app_id, user_id),
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);

CREATE TABLE credit_ledger (
    id               TEXT PRIMARY KEY,
    app_id           TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    entry_type       TEXT NOT NULL CHECK (entry_type IN (
                         'signup_bonus', 'referral_bonus', 'stripe_purchase', 'iap_purchase', 'ai_consume',
                         'refund', 'refund_reversal', 'affiliate_payout', 'affiliate_reversal', 'admin_adjust')),
    amount           INTEGER NOT NULL CHECK (amount <> 0),
    balance_after    INTEGER NOT NULL,
    idempotency_key  TEXT NOT NULL,
    reference_type   TEXT NULL,
    reference_id     TEXT NULL,
    actor            TEXT NOT NULL,
    reason           TEXT NOT NULL DEFAULT '',
    metadata         TEXT NOT NULL DEFAULT '{}',
    created_at       TEXT NOT NULL,
    CONSTRAINT uq_ledger_idem UNIQUE (app_id, idempotency_key),
    CONSTRAINT ck_ledger_sign CHECK (
        (entry_type IN ('signup_bonus', 'referral_bonus', 'stripe_purchase', 'iap_purchase',
                        'refund_reversal', 'affiliate_payout') AND amount > 0) OR
        (entry_type IN ('ai_consume', 'refund', 'affiliate_reversal') AND amount < 0) OR
        (entry_type = 'admin_adjust')
    ),
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);
CREATE INDEX idx_ledger_user ON credit_ledger (app_id, user_id, created_at DESC, id DESC);
CREATE INDEX idx_ledger_ref  ON credit_ledger (app_id, reference_type, reference_id);

CREATE TRIGGER trg_credit_ledger_no_update BEFORE UPDATE ON credit_ledger
BEGIN SELECT RAISE(ABORT, 'credit_ledger is append-only'); END;

CREATE TRIGGER trg_credit_ledger_no_delete BEFORE DELETE ON credit_ledger
BEGIN SELECT RAISE(ABORT, 'credit_ledger is append-only'); END;

CREATE TABLE referrals (
    id                TEXT PRIMARY KEY,
    app_id            TEXT NOT NULL,
    referrer_user_id  TEXT NOT NULL,
    referee_user_id   TEXT NOT NULL,
    code              TEXT NOT NULL,
    status            TEXT NOT NULL CHECK (status IN ('pending', 'rewarded', 'void')),
    rewarded_credits  INTEGER NOT NULL DEFAULT 0,
    reward_count      INTEGER NOT NULL DEFAULT 0,
    void_reason       TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    CONSTRAINT uq_referrals_referee UNIQUE (app_id, referee_user_id),
    CONSTRAINT ck_referrals_not_self CHECK (referrer_user_id <> referee_user_id),
    FOREIGN KEY (app_id, referrer_user_id) REFERENCES users (app_id, id),
    FOREIGN KEY (app_id, referee_user_id)  REFERENCES users (app_id, id)
);
CREATE INDEX idx_referrals_referrer ON referrals (app_id, referrer_user_id, created_at DESC);

CREATE TABLE files (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL,
    user_id        TEXT NOT NULL,
    connection_id  TEXT NOT NULL,
    bucket         TEXT NOT NULL,
    object_key     TEXT NOT NULL,
    filename       TEXT NOT NULL,
    content_type   TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL CHECK (size_bytes > 0),
    status         TEXT NOT NULL CHECK (status IN ('pending', 'uploaded', 'deleted')),
    created_at     TEXT NOT NULL,
    uploaded_at    TEXT NULL,
    deleted_at     TEXT NULL,
    FOREIGN KEY (app_id, user_id) REFERENCES users (app_id, id)
);
CREATE INDEX idx_files_user ON files (app_id, user_id, status, created_at DESC);
CREATE UNIQUE INDEX uq_files_object ON files (bucket, object_key);

CREATE TABLE outbox (
    id               TEXT PRIMARY KEY,
    app_id           TEXT NULL,
    event_type       TEXT NOT NULL,
    subscriber       TEXT NOT NULL,
    payload_enc      BLOB NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('pending', 'done', 'skipped', 'dead')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  TEXT NOT NULL,
    locked_until     TEXT NULL,
    locked_by        TEXT NULL,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    finished_at      TEXT NULL
);
CREATE INDEX idx_outbox_due ON outbox (status, next_attempt_at) WHERE status = 'pending';

CREATE TABLE job_state (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE admin_audit_log (
    id           TEXT PRIMARY KEY,
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    app_id       TEXT NULL,
    target_type  TEXT NOT NULL,
    target_id    TEXT NOT NULL,
    diff         TEXT NOT NULL DEFAULT '{}',
    ip           TEXT NOT NULL,
    request_id   TEXT NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_audit_app ON admin_audit_log (app_id, created_at DESC);
