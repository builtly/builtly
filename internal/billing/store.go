package billing

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists purchases and webhook events.
type Store struct{ db *db.DB }

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectPurchase = `SELECT id, app_id, user_id, product_id, provider, connection_id, provider_ref, payment_ref,
	status, credits, refunded_credits, amount, currency, environment, affiliate_ref, provider_data,
	created_at, updated_at, completed_at FROM purchases`

// InsertPurchase inserts a purchase row.
func (s *Store) InsertPurchase(ctx context.Context, q db.Querier, p Purchase) error {
	data := p.ProviderData
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO purchases (
			id, app_id, user_id, product_id, provider, connection_id, provider_ref, payment_ref,
			status, credits, refunded_credits, amount, currency, environment, affiliate_ref, provider_data,
			created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		p.ID, p.AppID, p.UserID, p.ProductID, p.Provider, p.ConnectionID, p.ProviderRef, p.PaymentRef,
		p.Status, p.Credits, p.RefundedCredits, p.Amount, p.Currency, p.Environment, p.AffiliateRef, data,
		p.CreatedAt, p.UpdatedAt, p.CompletedAt)
	return db.MapError(err)
}

// GetPurchaseByID loads a purchase.
func (s *Store) GetPurchaseByID(ctx context.Context, q db.Querier, appID, id uuid.UUID) (Purchase, error) {
	var p Purchase
	err := s.q(q).GetContext(ctx, &p, s.db.Rebind(selectPurchase+` WHERE app_id = ? AND id = ?`), appID, id)
	return p, db.MapError(err)
}

// GetPurchaseByProviderRef loads by provider ref.
func (s *Store) GetPurchaseByProviderRef(ctx context.Context, q db.Querier, appID uuid.UUID, provider, ref string) (Purchase, error) {
	var p Purchase
	err := s.q(q).GetContext(ctx, &p, s.db.Rebind(selectPurchase+`
		WHERE app_id = ? AND provider = ? AND provider_ref = ?`), appID, provider, ref)
	return p, db.MapError(err)
}

// GetPurchaseByPaymentRef finds purchase by payment_intent.
func (s *Store) GetPurchaseByPaymentRef(ctx context.Context, q db.Querier, provider, paymentRef string) (Purchase, error) {
	var p Purchase
	err := s.q(q).GetContext(ctx, &p, s.db.Rebind(selectPurchase+`
		WHERE provider = ? AND payment_ref = ?`), provider, paymentRef)
	return p, db.MapError(err)
}

// UpdatePurchaseAfterSession sets provider_ref after Stripe session create.
func (s *Store) UpdatePurchaseAfterSession(ctx context.Context, q db.Querier, appID, id uuid.UUID, ref, status string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE purchases SET provider_ref = ?, status = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		ref, status, now, appID, id)
	return db.MapError(err)
}

// MarkPurchaseCompleted marks a pending purchase completed.
func (s *Store) MarkPurchaseCompleted(ctx context.Context, q db.Querier, appID, id uuid.UUID, amount int64, currency, paymentRef string, now db.Time) (bool, error) {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE purchases SET status = ?, amount = ?, currency = ?, payment_ref = ?,
			completed_at = ?, updated_at = ?
		WHERE app_id = ? AND id = ? AND status = ?`),
		StatusCompleted, amount, currency, paymentRef, now, now, appID, id, StatusPending)
	if err != nil {
		return false, db.MapError(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkPurchaseStatus transitions status when matching fromStatus.
func (s *Store) MarkPurchaseStatus(ctx context.Context, q db.Querier, appID, id uuid.UUID, from, to string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE purchases SET status = ?, updated_at = ? WHERE app_id = ? AND id = ? AND status = ?`),
		to, now, appID, id, from)
	return db.MapError(err)
}

// SetRefundedCredits updates refunded_credits and status.
func (s *Store) SetRefundedCredits(ctx context.Context, q db.Querier, appID, id uuid.UUID, refunded int64, status string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE purchases SET refunded_credits = ?, status = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		refunded, status, now, appID, id)
	return db.MapError(err)
}

// PurchaseListQuery filters an app-wide purchase list. UserQ is an email or user-id prefix.
// After/AfterID empty means the first page; order is created_at DESC, id DESC.
type PurchaseListQuery struct {
	Status   string
	Provider string
	UserID   *uuid.UUID
	UserQ    string
	Limit    int
	After    db.Time
	AfterID  uuid.UUID
}

func purchaseListWhere(appID uuid.UUID, f PurchaseListQuery) (string, []any) {
	where := `WHERE app_id = ?`
	args := []any{appID}
	if f.Status != "" {
		where += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Provider != "" {
		where += ` AND provider = ?`
		args = append(args, f.Provider)
	}
	if f.UserID != nil {
		where += ` AND user_id = ?`
		args = append(args, *f.UserID)
	}
	if f.UserQ != "" {
		like := f.UserQ + "%"
		where += ` AND EXISTS (
			SELECT 1 FROM users
			WHERE users.app_id = purchases.app_id AND users.id = purchases.user_id
			  AND (users.email LIKE ? OR CAST(users.id AS TEXT) LIKE ?)
		)`
		args = append(args, like, like)
	}
	return where, args
}

// CountAppPurchases returns how many purchases match the filter (ignores cursor/limit).
func (s *Store) CountAppPurchases(ctx context.Context, q db.Querier, appID uuid.UUID, f PurchaseListQuery) (int, error) {
	where, args := purchaseListWhere(appID, f)
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM purchases /* app_id */ `+where), args...)
	if err != nil {
		return 0, db.MapError(err)
	}
	return n, nil
}

// ListAppPurchases lists purchases for one app, newest first.
func (s *Store) ListAppPurchases(ctx context.Context, q db.Querier, appID uuid.UUID, f PurchaseListQuery) ([]Purchase, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	where, args := purchaseListWhere(appID, f)
	if f.AfterID != uuid.Nil {
		where += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, f.After, f.After, f.AfterID)
	}
	args = append(args, f.Limit)
	var rows []Purchase
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPurchase+` `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`), args...)
	return rows, db.MapError(err)
}

// ListPurchases lists purchases for an app.
func (s *Store) ListPurchases(ctx context.Context, q db.Querier, appID uuid.UUID, status, provider string, limit int) ([]Purchase, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []Purchase
	var err error
	switch {
	case status != "" && provider != "":
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPurchase+`
			WHERE app_id = ? AND status = ? AND provider = ? ORDER BY created_at DESC LIMIT ?`), appID, status, provider, limit)
	case status != "":
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPurchase+`
			WHERE app_id = ? AND status = ? ORDER BY created_at DESC LIMIT ?`), appID, status, limit)
	case provider != "":
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPurchase+`
			WHERE app_id = ? AND provider = ? ORDER BY created_at DESC LIMIT ?`), appID, provider, limit)
	default:
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPurchase+`
			WHERE app_id = ? ORDER BY created_at DESC LIMIT ?`), appID, limit)
	}
	return rows, db.MapError(err)
}

const selectWebhook = `SELECT id, provider, connection_id, event_id, event_type, app_id, status, attempts, last_error, received_at, processed_at FROM webhook_events`

// TryInsertWebhookEvent inserts a webhook event; returns (event, inserted).
func (s *Store) TryInsertWebhookEvent(ctx context.Context, q db.Querier, ev WebhookEvent) (WebhookEvent, bool, error) {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO webhook_events (
			id, provider, connection_id, event_id, event_type, app_id, status, attempts, last_error, received_at, processed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, connection_id, event_id) DO NOTHING`),
		ev.ID, ev.Provider, ev.ConnectionID, ev.EventID, ev.EventType, ev.AppID, ev.Status, ev.Attempts, ev.LastError, ev.ReceivedAt, ev.ProcessedAt)
	if err != nil {
		return WebhookEvent{}, false, db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		return ev, true, nil
	}
	existing, err := s.GetWebhookEvent(ctx, q, ev.Provider, ev.ConnectionID, ev.EventID)
	return existing, false, err
}

// ExpireStalePendingPurchases marks pending purchases older than cutoff as expired.
func (s *Store) ExpireStalePendingPurchases(ctx context.Context, cutoff, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE purchases SET status = ?, updated_at = ?
		WHERE app_id IS NOT NULL AND status = ? AND created_at < ?`),
		StatusExpired, db.Time(now.UTC()), StatusPending, db.Time(cutoff.UTC()))
	return db.MapError(err)
}

// HasCompletedPurchase reports whether the user has any completed/refunded purchase.
func (s *Store) HasCompletedPurchase(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (bool, error) {
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`
		SELECT COUNT(*) FROM purchases
		WHERE app_id = ? AND user_id = ? AND status IN (?, ?, ?)`),
		appID, userID, StatusCompleted, StatusPartialRefunded, StatusRefunded)
	return n > 0, db.MapError(err)
}

// GetWebhookEvent loads by unique key.
func (s *Store) GetWebhookEvent(ctx context.Context, q db.Querier, provider, connectionID, eventID string) (WebhookEvent, error) {
	var ev WebhookEvent
	err := s.q(q).GetContext(ctx, &ev, s.db.Rebind(selectWebhook+`
		WHERE provider = ? AND connection_id = ? AND event_id = ?`), provider, connectionID, eventID)
	return ev, db.MapError(err)
}

// BumpWebhookAttempt increments attempts and optionally sets last_error.
func (s *Store) BumpWebhookAttempt(ctx context.Context, q db.Querier, id uuid.UUID, lastError string) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE webhook_events SET attempts = attempts + 1, last_error = ? WHERE id = ?`), lastError, id)
	return db.MapError(err)
}

// FinishWebhookEvent sets terminal status.
func (s *Store) FinishWebhookEvent(ctx context.Context, q db.Querier, id uuid.UUID, status, lastError string, appID *uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE webhook_events SET status = ?, last_error = ?, app_id = ?, processed_at = ? WHERE id = ?`),
		status, lastError, appID, now, id)
	return db.MapError(err)
}
