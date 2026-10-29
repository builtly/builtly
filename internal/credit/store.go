package credit

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists balances and ledger rows.
type Store struct {
	db *db.DB
}

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

// GetBalance returns the balance row or ErrNotFound.
func (s *Store) GetBalance(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (Balance, error) {
	var b Balance
	err := s.q(q).GetContext(ctx, &b, s.db.Rebind(`
		SELECT app_id, user_id, balance, updated_at FROM credit_balances
		WHERE app_id = ? AND user_id = ?`), appID, userID)
	if err != nil {
		return Balance{}, db.MapError(err)
	}
	return b, nil
}

// GetBalanceOrZero returns balance amount, treating missing row as 0.
func (s *Store) GetBalanceOrZero(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (int64, error) {
	b, err := s.GetBalance(ctx, q, appID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return b.Balance, nil
}

// GetEntryByIdempotencyKey loads a ledger entry by app + key.
func (s *Store) GetEntryByIdempotencyKey(ctx context.Context, q db.Querier, appID uuid.UUID, key string) (Entry, error) {
	var e Entry
	err := s.q(q).GetContext(ctx, &e, s.db.Rebind(`
		SELECT id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
		       reference_type, reference_id, actor, reason, metadata, created_at
		FROM credit_ledger WHERE app_id = ? AND idempotency_key = ?`), appID, key)
	if err != nil {
		return Entry{}, db.MapError(err)
	}
	return e, nil
}

// DeductIfSufficient atomically subtracts amount when balance >= amount; returns new balance.
func (s *Store) DeductIfSufficient(ctx context.Context, q db.Querier, appID, userID uuid.UUID, amount int64, now db.Time) (int64, error) {
	var bal int64
	err := s.q(q).GetContext(ctx, &bal, s.db.Rebind(`
		UPDATE credit_balances SET balance = balance - ?, updated_at = ?
		WHERE app_id = ? AND user_id = ? AND balance >= ?
		RETURNING balance`), amount, now, appID, userID, amount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(db.MapError(err), db.ErrNotFound) {
			return 0, db.ErrNotFound
		}
		return 0, db.MapError(err)
	}
	return bal, nil
}

// ApplyBalanceDelta upserts balance by adding amount (signed) and returns new balance.
func (s *Store) ApplyBalanceDelta(ctx context.Context, q db.Querier, appID, userID uuid.UUID, amount int64, now db.Time) (int64, error) {
	var bal int64
	err := s.q(q).GetContext(ctx, &bal, s.db.Rebind(`
		INSERT INTO credit_balances (app_id, user_id, balance, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (app_id, user_id) DO UPDATE
		SET balance = credit_balances.balance + excluded.balance, updated_at = excluded.updated_at
		RETURNING balance`), appID, userID, amount, now)
	if err != nil {
		return 0, db.MapError(err)
	}
	return bal, nil
}

// InsertEntry appends a ledger row.
func (s *Store) InsertEntry(ctx context.Context, q db.Querier, e Entry) error {
	meta := e.Metadata
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO credit_ledger (
			id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
			reference_type, reference_id, actor, reason, metadata, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		e.ID, e.AppID, e.UserID, e.EntryType, e.Amount, e.BalanceAfter, e.IdempotencyKey,
		e.ReferenceType, e.ReferenceID, e.Actor, e.Reason, meta, e.CreatedAt)
	return db.MapError(err)
}

// GetByReference loads one ledger entry by type + reference.
func (s *Store) GetByReference(ctx context.Context, q db.Querier, appID uuid.UUID, entryType, refType, refID string) (Entry, error) {
	var e Entry
	err := s.q(q).GetContext(ctx, &e, s.db.Rebind(`
		SELECT id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
		       reference_type, reference_id, actor, reason, metadata, created_at
		FROM credit_ledger
		WHERE app_id = ? AND entry_type = ? AND reference_type = ? AND reference_id = ?
		ORDER BY created_at ASC, id ASC LIMIT 1`), appID, entryType, refType, refID)
	return e, db.MapError(err)
}

// SumReversalAmount returns the total absolute amount of affiliate_reversal for a reference.
func (s *Store) SumReversalAmount(ctx context.Context, q db.Querier, appID uuid.UUID, refType, refID string) (int64, error) {
	var n int64
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`
		SELECT COALESCE(SUM(-amount), 0) FROM credit_ledger
		WHERE app_id = ? AND entry_type = ? AND reference_type = ? AND reference_id = ?`),
		appID, TypeAffiliateReversal, refType, refID)
	return n, db.MapError(err)
}

// AppLedgerQuery filters an app-wide ledger list. UserQ is an email or user-id prefix.
// After/AfterID empty means the first page; order is created_at DESC, id DESC.
type AppLedgerQuery struct {
	EntryType string
	UserID    *uuid.UUID
	UserQ     string
	Limit     int
	After     db.Time
	AfterID   uuid.UUID
}

func appLedgerWhere(appID uuid.UUID, f AppLedgerQuery) (string, []any) {
	where := `WHERE app_id = ?`
	args := []any{appID}
	if f.EntryType != "" {
		where += ` AND entry_type = ?`
		args = append(args, f.EntryType)
	}
	if f.UserID != nil {
		where += ` AND user_id = ?`
		args = append(args, *f.UserID)
	}
	if f.UserQ != "" {
		like := f.UserQ + "%"
		where += ` AND EXISTS (
			SELECT 1 FROM users
			WHERE users.app_id = credit_ledger.app_id AND users.id = credit_ledger.user_id
			  AND (users.email LIKE ? OR CAST(users.id AS TEXT) LIKE ?)
		)`
		args = append(args, like, like)
	}
	return where, args
}

const selectAppLedger = `
		SELECT id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
		       reference_type, reference_id, actor, reason, metadata, created_at
		FROM credit_ledger `

// CountAppEntries returns how many ledger rows match the filter (ignores cursor/limit).
func (s *Store) CountAppEntries(ctx context.Context, q db.Querier, appID uuid.UUID, f AppLedgerQuery) (int, error) {
	where, args := appLedgerWhere(appID, f)
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM credit_ledger /* app_id */ `+where), args...)
	if err != nil {
		return 0, db.MapError(err)
	}
	return n, nil
}

// ListAppEntries returns ledger rows for an app, newest first.
func (s *Store) ListAppEntries(ctx context.Context, q db.Querier, appID uuid.UUID, f AppLedgerQuery) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	where, args := appLedgerWhere(appID, f)
	if f.AfterID != uuid.Nil {
		where += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, f.After, f.After, f.AfterID)
	}
	args = append(args, f.Limit)
	var rows []Entry
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectAppLedger+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`), args...)
	return rows, db.MapError(err)
}

// ListEntries returns ledger rows for a user, newest first.
func (s *Store) ListEntries(ctx context.Context, q db.Querier, appID, userID uuid.UUID, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []Entry
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
		       reference_type, reference_id, actor, reason, metadata, created_at
		FROM credit_ledger WHERE app_id = ? AND user_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`), appID, userID, limit)
	return rows, db.MapError(err)
}

// SumLedger returns SUM(amount) for a user.
func (s *Store) SumLedger(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (int64, error) {
	var sum sql.NullInt64
	err := s.q(q).GetContext(ctx, &sum, s.db.Rebind(`
		SELECT COALESCE(SUM(amount), 0) FROM credit_ledger WHERE app_id = ? AND user_id = ?`), appID, userID)
	if err != nil {
		return 0, db.MapError(err)
	}
	return sum.Int64, nil
}

// LatestBalanceAfter returns the newest balance_after or ok=false.
func (s *Store) LatestBalanceAfter(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (int64, bool, error) {
	var bal int64
	err := s.q(q).GetContext(ctx, &bal, s.db.Rebind(`
		SELECT balance_after FROM credit_ledger
		WHERE app_id = ? AND user_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`), appID, userID)
	if err != nil {
		if errors.Is(db.MapError(err), db.ErrNotFound) {
			return 0, false, nil
		}
		return 0, false, db.MapError(err)
	}
	return bal, true, nil
}

// ListBalanceUsers returns all (app_id, user_id) pairs that have a balance or ledger.
func (s *Store) ListBalanceUsers(ctx context.Context, q db.Querier, appID *uuid.UUID) ([]Balance, error) {
	var rows []Balance
	var err error
	if appID != nil {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT app_id, user_id, balance, updated_at FROM credit_balances WHERE app_id = ?`), *appID)
	} else {
		err = s.q(q).SelectContext(ctx, &rows, `SELECT app_id, user_id, balance, updated_at FROM credit_balances`)
	}
	return rows, db.MapError(err)
}

// CASZeroBalance sets balance to 0 only if it currently equals observed.
func (s *Store) CASZeroBalance(ctx context.Context, q db.Querier, appID, userID uuid.UUID, observed int64, now db.Time) (bool, error) {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE credit_balances SET balance = 0, updated_at = ?
		WHERE app_id = ? AND user_id = ? AND balance = ?`), now, appID, userID, observed)
	if err != nil {
		return false, db.MapError(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
