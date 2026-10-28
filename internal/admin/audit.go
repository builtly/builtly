package admin

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
)

// AuditEntry is one admin mutation record.
type AuditEntry struct {
	ID         uuid.UUID       `db:"id" json:"id"`
	Actor      string          `db:"actor" json:"actor"`
	Action     string          `db:"action" json:"action"`
	AppID      *uuid.UUID      `db:"app_id" json:"app_id,omitempty"`
	TargetType string          `db:"target_type" json:"target_type"`
	TargetID   string          `db:"target_id" json:"target_id"`
	Diff       json.RawMessage `db:"diff" json:"diff"`
	IP         string          `db:"ip" json:"ip"`
	RequestID  string          `db:"request_id" json:"request_id"`
	CreatedAt  db.Time         `db:"created_at" json:"created_at"`
}

// AuditStore persists admin_audit_log rows.
type AuditStore struct {
	db *db.DB
}

// NewAuditStore constructs an AuditStore.
func NewAuditStore(database *db.DB) *AuditStore {
	return &AuditStore{db: database}
}

// Insert writes an audit row (diff should already be redacted).
func (s *AuditStore) Insert(ctx context.Context, q db.Querier, e AuditEntry) error {
	if q == nil {
		q = s.db
	}
	if len(e.Diff) == 0 {
		e.Diff = json.RawMessage(`{}`)
	}
	_, err := q.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO admin_audit_log (id, actor, action, app_id, target_type, target_id, diff, ip, request_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		e.ID, e.Actor, e.Action, e.AppID, e.TargetType, e.TargetID, []byte(e.Diff), e.IP, e.RequestID, e.CreatedAt)
	return db.MapError(err)
}

// List returns recent audit entries with optional filters.
func (s *AuditStore) List(ctx context.Context, appID *uuid.UUID, action string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows []struct {
			ID         uuid.UUID  `db:"id"`
			Actor      string     `db:"actor"`
			Action     string     `db:"action"`
			AppID      *uuid.UUID `db:"app_id"`
			TargetType string     `db:"target_type"`
			TargetID   string     `db:"target_id"`
			Diff       []byte     `db:"diff"`
			IP         string     `db:"ip"`
			RequestID  string     `db:"request_id"`
			CreatedAt  db.Time    `db:"created_at"`
		}
		err error
	)
	switch {
	case appID != nil && action != "":
		err = s.db.SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, actor, action, app_id, target_type, target_id, diff, ip, request_id, created_at
			FROM admin_audit_log WHERE app_id = ? AND action = ?
			ORDER BY created_at DESC LIMIT ?`), *appID, action, limit)
	case appID != nil:
		err = s.db.SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, actor, action, app_id, target_type, target_id, diff, ip, request_id, created_at
			FROM admin_audit_log WHERE app_id = ?
			ORDER BY created_at DESC LIMIT ?`), *appID, limit)
	case action != "":
		err = s.db.SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, actor, action, app_id, target_type, target_id, diff, ip, request_id, created_at
			FROM admin_audit_log WHERE action = ?
			ORDER BY created_at DESC LIMIT ?`), action, limit)
	default:
		err = s.db.SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, actor, action, app_id, target_type, target_id, diff, ip, request_id, created_at
			FROM admin_audit_log ORDER BY created_at DESC LIMIT ?`), limit)
	}
	if err != nil {
		return nil, db.MapError(err)
	}
	out := make([]AuditEntry, len(rows))
	for i, r := range rows {
		out[i] = AuditEntry{
			ID: r.ID, Actor: r.Actor, Action: r.Action, AppID: r.AppID,
			TargetType: r.TargetType, TargetID: r.TargetID, Diff: json.RawMessage(r.Diff),
			IP: r.IP, RequestID: r.RequestID, CreatedAt: r.CreatedAt,
		}
	}
	return out, nil
}

// RedactDiffMap returns a copy of diff with secrets replaced by "[redacted]".
func RedactDiffMap(diff map[string]any) map[string]any {
	return integration.RedactDiff(diff)
}

// MustJSON marshals v or returns {}.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Ensure sqlx.Tx satisfies Querier at compile time via admin package usage.
var _ db.Querier = (*sqlx.Tx)(nil)
