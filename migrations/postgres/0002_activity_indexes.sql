CREATE INDEX idx_purchases_app_created ON purchases (app_id, created_at DESC, id DESC);
CREATE INDEX idx_ledger_app_created ON credit_ledger (app_id, created_at DESC, id DESC);
CREATE INDEX idx_ledger_app_type ON credit_ledger (app_id, entry_type, created_at DESC, id DESC);
