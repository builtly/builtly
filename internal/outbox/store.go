package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists outbox rows.
type Store struct {
	db *db.DB
}

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectRow = `SELECT id, app_id, event_type, subscriber, payload_enc, status, attempts,
	next_attempt_at, locked_until, locked_by, last_error, created_at, finished_at FROM outbox`

// Insert adds a pending row.
func (s *Store) Insert(ctx context.Context, q db.Querier, row Row) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO outbox (
			id, app_id, event_type, subscriber, payload_enc, status, attempts,
			next_attempt_at, locked_until, locked_by, last_error, created_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		row.ID, row.AppID, row.EventType, row.Subscriber, row.PayloadEnc, row.Status, row.Attempts,
		row.NextAttempt, row.LockedUntil, row.LockedBy, row.LastError, row.CreatedAt, row.FinishedAt)
	return db.MapError(err)
}

// ClaimDue claims one due pending row for the instance (lease 60s).
func (s *Store) ClaimDue(ctx context.Context, instanceID string, now time.Time) (Row, error) {
	leaseUntil := now.Add(60 * time.Second)
	nowDB := db.Time(now.UTC())
	leaseDB := db.Time(leaseUntil.UTC())

	// Dialect-portable claim: select candidate then update if still claimable.
	var id uuid.UUID
	err := s.db.GetContext(ctx, &id, s.db.Rebind(`
		SELECT id FROM outbox
		WHERE status = 'pending' AND next_attempt_at <= ?
		  AND (locked_until IS NULL OR locked_until < ?)
		ORDER BY next_attempt_at LIMIT 1`), nowDB, nowDB)
	if err != nil {
		return Row{}, db.MapError(err)
	}

	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET locked_until = ?, locked_by = ?, attempts = attempts + 1
		WHERE id = ? AND status = 'pending'
		  AND (locked_until IS NULL OR locked_until < ?)`),
		leaseDB, instanceID, id, nowDB)
	if err != nil {
		return Row{}, db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Row{}, db.ErrNotFound
	}
	return s.GetByID(ctx, nil, id)
}

// GetByID loads a row.
func (s *Store) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (Row, error) {
	var row Row
	err := s.q(q).GetContext(ctx, &row, s.db.Rebind(selectRow+` WHERE id = ?`), id)
	if err != nil {
		return Row{}, db.MapError(err)
	}
	return row, nil
}

// MarkDone sets status done.
func (s *Store) MarkDone(ctx context.Context, id uuid.UUID, now db.Time) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET status = 'done', finished_at = ?, locked_until = NULL, locked_by = NULL, last_error = ''
		WHERE id = ?`), now, id)
	return db.MapError(err)
}

// MarkSkipped sets status skipped.
func (s *Store) MarkSkipped(ctx context.Context, id uuid.UUID, now db.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET status = 'skipped', finished_at = ?, locked_until = NULL, locked_by = NULL, last_error = ?
		WHERE id = ?`), now, truncateErr(reason), id)
	return db.MapError(err)
}

// MarkDead sets status dead.
func (s *Store) MarkDead(ctx context.Context, id uuid.UUID, now db.Time, lastErr string) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET status = 'dead', finished_at = ?, locked_until = NULL, locked_by = NULL, last_error = ?
		WHERE id = ?`), now, truncateErr(lastErr), id)
	return db.MapError(err)
}

// MarkRetry schedules another attempt.
func (s *Store) MarkRetry(ctx context.Context, id uuid.UUID, next db.Time, lastErr string) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET status = 'pending', next_attempt_at = ?, locked_until = NULL, locked_by = NULL, last_error = ?
		WHERE id = ?`), next, truncateErr(lastErr), id)
	return db.MapError(err)
}

// RetryDead resets a dead row to pending.
func (s *Store) RetryDead(ctx context.Context, id uuid.UUID, now db.Time) error {
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE outbox SET status = 'pending', attempts = 0, next_attempt_at = ?,
			locked_until = NULL, locked_by = NULL, last_error = '', finished_at = NULL
		WHERE id = ? AND status = 'dead'`), now, id)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

func outboxListWhere(status, subscriber string) (string, []any) {
	where := `WHERE 1=1`
	var args []any
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	if subscriber != "" {
		where += ` AND subscriber = ?`
		args = append(args, subscriber)
	}
	return where, args
}

// Count returns how many outbox rows match the filters.
func (s *Store) Count(ctx context.Context, status, subscriber string) (int, error) {
	where, args := outboxListWhere(status, subscriber)
	var n int
	err := s.db.GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM outbox `+where), args...)
	if err != nil {
		return 0, db.MapError(err)
	}
	return n, nil
}

// List filters outbox rows with optional keyset cursor (created_at DESC, id DESC).
func (s *Store) List(ctx context.Context, status, subscriber string, limit int, after db.Time, afterID uuid.UUID) ([]Row, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where, args := outboxListWhere(status, subscriber)
	if afterID != uuid.Nil {
		where += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, after, after, afterID)
	}
	args = append(args, limit)
	var rows []Row
	err := s.db.SelectContext(ctx, &rows, s.db.Rebind(selectRow+` `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`), args...)
	return rows, db.MapError(err)
}

func truncateErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max]
}
