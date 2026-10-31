package credit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/platform/db"
)

// ConsumeHook is called inside the Consume transaction after a new ledger entry is inserted.
type ConsumeHook func(ctx context.Context, tx *sqlx.Tx, entry Entry, feature string) error

// Service is the only writer of credit_balances / credit_ledger.
type Service struct {
	store       *Store
	db          *db.DB
	consumeHook ConsumeHook
}

// NewService constructs a credit Service.
func NewService(database *db.DB, store *Store) *Service {
	return &Service{store: store, db: database}
}

// SetConsumeHook registers an optional transaction hook invoked on non-replayed consumes.
func (s *Service) SetConsumeHook(fn ConsumeHook) {
	s.consumeHook = fn
}

// GetBalance returns the current balance (0 if no row).
func (s *Service) GetBalance(ctx context.Context, appID, userID uuid.UUID) (Balance, error) {
	b, err := s.store.GetBalance(ctx, nil, appID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Balance{AppID: appID, UserID: userID, Balance: 0, UpdatedAt: db.Now()}, nil
		}
		return Balance{}, err
	}
	return b, nil
}

// ListLedger returns recent ledger entries.
func (s *Service) ListLedger(ctx context.Context, appID, userID uuid.UUID, limit int) ([]Entry, error) {
	return s.store.ListEntries(ctx, nil, appID, userID, limit)
}

// ListLedgerPaged returns recent ledger entries for a user with optional keyset cursor.
func (s *Service) ListLedgerPaged(ctx context.Context, appID, userID uuid.UUID, limit int, after db.Time, afterID uuid.UUID) ([]Entry, error) {
	return s.store.ListEntriesPaged(ctx, nil, appID, userID, limit, after, afterID)
}

// GetByIdempotencyKey loads a ledger entry by app + idempotency key.
func (s *Service) GetByIdempotencyKey(ctx context.Context, appID uuid.UUID, key string) (Entry, error) {
	return s.store.GetEntryByIdempotencyKey(ctx, nil, appID, key)
}

// GetByIdempotencyKeyTx loads a ledger entry by app + idempotency key inside an existing transaction.
func (s *Service) GetByIdempotencyKeyTx(ctx context.Context, q db.Querier, appID uuid.UUID, key string) (Entry, error) {
	return s.store.GetEntryByIdempotencyKey(ctx, q, appID, key)
}

// ListAppLedger returns recent ledger entries for an app.
func (s *Service) ListAppLedger(ctx context.Context, appID uuid.UUID, filter AppLedgerQuery) ([]Entry, error) {
	return s.store.ListAppEntries(ctx, nil, appID, filter)
}

// CountAppLedger counts ledger entries for an app under the same filters as ListAppLedger.
func (s *Service) CountAppLedger(ctx context.Context, appID uuid.UUID, filter AppLedgerQuery) (int, error) {
	return s.store.CountAppEntries(ctx, nil, appID, filter)
}

// GetByReference loads a ledger entry by type and reference.
func (s *Service) GetByReference(ctx context.Context, appID uuid.UUID, entryType, refType, refID string) (Entry, error) {
	return s.store.GetByReference(ctx, nil, appID, entryType, refType, refID)
}

// SumReversals returns absolute affiliate_reversal total for a purchase reference.
func (s *Service) SumReversals(ctx context.Context, appID uuid.UUID, refType, refID string) (int64, error) {
	return s.store.SumReversalAmount(ctx, nil, appID, refType, refID)
}

// Consume conditionally deducts credits for AI metering (idempotent).
func (s *Service) Consume(ctx context.Context, appID, userID uuid.UUID, amount int64, idempotencyKey, feature, actor string, metadata json.RawMessage) (ConsumeResult, error) {
	if amount < 1 || amount > 1_000_000 {
		return ConsumeResult{}, ErrInvalidAmount
	}
	if idempotencyKey == "" {
		return ConsumeResult{}, ErrMissingIdempotency
	}
	key := "ai:" + userID.String() + ":" + idempotencyKey
	meta := metadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	// embed feature in metadata for conflict detection
	var metaMap map[string]any
	_ = json.Unmarshal(meta, &metaMap)
	if metaMap == nil {
		metaMap = map[string]any{}
	}
	metaMap["feature"] = feature
	meta, _ = json.Marshal(metaMap)

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var result ConsumeResult
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			existing, err := s.store.GetEntryByIdempotencyKey(ctx, tx, appID, key)
			if err == nil {
				if existing.UserID != userID || existing.Amount != -amount {
					return ErrIdempotencyConflict
				}
				var prev map[string]any
				_ = json.Unmarshal(existing.Metadata, &prev)
				if feat, _ := prev["feature"].(string); feat != feature {
					return ErrIdempotencyConflict
				}
				result = ConsumeResult{Entry: existing, Balance: existing.BalanceAfter, Replayed: true}
				return nil
			}
			if !errors.Is(err, db.ErrNotFound) {
				return err
			}

			now := db.Now()
			newBal, err := s.store.DeductIfSufficient(ctx, tx, appID, userID, amount, now)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					bal, _ := s.store.GetBalanceOrZero(ctx, tx, appID, userID)
					return ErrInsufficientCredits.WithDetails(map[string]any{
						"balance": bal, "required": amount,
					})
				}
				return err
			}

			refType := "ai_request"
			refID := idempotencyKey
			e := Entry{
				ID: uuid.Must(uuid.NewV7()), AppID: appID, UserID: userID,
				EntryType: TypeAIConsume, Amount: -amount, BalanceAfter: newBal,
				IdempotencyKey: key, ReferenceType: &refType, ReferenceID: &refID,
				Actor: actor, Metadata: meta, CreatedAt: now,
			}
			if err := s.store.InsertEntry(ctx, tx, e); err != nil {
				var u db.ErrUniqueViolation
				if errors.As(err, &u) {
					return errRetryConsume
				}
				return err
			}
			if s.consumeHook != nil {
				if err := s.consumeHook(ctx, tx, e, feature); err != nil {
					return err
				}
			}
			result = ConsumeResult{Entry: e, Balance: newBal, Replayed: false}
			return nil
		})
		if errors.Is(err, errRetryConsume) {
			continue
		}
		if err != nil {
			return ConsumeResult{}, err
		}
		return result, nil
	}
	return ConsumeResult{}, ErrConcurrentMod
}

var errRetryConsume = errors.New("credit: retry consume after concurrent idempotent insert")

// ApplyTx writes an unconditional ledger entry inside an existing transaction.
func (s *Service) ApplyTx(ctx context.Context, tx *sqlx.Tx, in ApplyInput) (ApplyResult, error) {
	if tx == nil {
		return ApplyResult{}, fmt.Errorf("credit: ApplyTx requires tx")
	}
	return s.apply(ctx, tx, in)
}

// Apply opens a transaction and applies an unconditional entry.
func (s *Service) Apply(ctx context.Context, in ApplyInput) (ApplyResult, error) {
	var out ApplyResult
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var err error
		out, err = s.apply(ctx, tx, in)
		return err
	})
	return out, err
}

func (s *Service) apply(ctx context.Context, q db.Querier, in ApplyInput) (ApplyResult, error) {
	if in.EntryType == TypeAIConsume {
		return ApplyResult{}, ErrInvalidEntryType.WithDetails(map[string]any{"reason": "use Consume for ai_consume"})
	}
	if in.Amount == 0 {
		return ApplyResult{}, ErrInvalidAmount
	}
	if in.IdempotencyKey == "" {
		return ApplyResult{}, ErrMissingIdempotency
	}
	if err := validateSign(in.EntryType, in.Amount); err != nil {
		return ApplyResult{}, err
	}
	if in.EntryType == TypeAdminAdjust && in.Reason == "" {
		return ApplyResult{}, ErrReasonRequired
	}

	existing, err := s.store.GetEntryByIdempotencyKey(ctx, q, in.AppID, in.IdempotencyKey)
	if err == nil {
		return ApplyResult{Entry: existing, Balance: existing.BalanceAfter, Applied: false}, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return ApplyResult{}, err
	}

	now := db.Now()
	newBal, err := s.store.ApplyBalanceDelta(ctx, q, in.AppID, in.UserID, in.Amount, now)
	if err != nil {
		return ApplyResult{}, err
	}
	meta := []byte(in.Metadata)
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	var refType, refID *string
	if in.ReferenceType != "" {
		refType = &in.ReferenceType
	}
	if in.ReferenceID != "" {
		refID = &in.ReferenceID
	}
	e := Entry{
		ID: uuid.Must(uuid.NewV7()), AppID: in.AppID, UserID: in.UserID,
		EntryType: in.EntryType, Amount: in.Amount, BalanceAfter: newBal,
		IdempotencyKey: in.IdempotencyKey, ReferenceType: refType, ReferenceID: refID,
		Actor: in.Actor, Reason: in.Reason, Metadata: meta, CreatedAt: now,
	}
	if err := s.store.InsertEntry(ctx, q, e); err != nil {
		var u db.ErrUniqueViolation
		if errors.As(err, &u) {
			existing, err2 := s.store.GetEntryByIdempotencyKey(ctx, q, in.AppID, in.IdempotencyKey)
			if err2 != nil {
				return ApplyResult{}, err2
			}
			return ApplyResult{Entry: existing, Balance: existing.BalanceAfter, Applied: false}, nil
		}
		return ApplyResult{}, err
	}
	return ApplyResult{Entry: e, Balance: newBal, Applied: true}, nil
}

func validateSign(entryType string, amount int64) error {
	switch entryType {
	case TypeSignupBonus, TypeReferralBonus, TypeStripePurchase, TypeIAPPurchase, TypeRefundReversal, TypeAffiliatePayout:
		if amount <= 0 {
			return ErrInvalidAmount
		}
	case TypeRefund, TypeAffiliateReversal:
		if amount >= 0 {
			return ErrInvalidAmount
		}
	case TypeAdminAdjust:
		// ± ok
	default:
		return ErrInvalidEntryType
	}
	return nil
}

// ClearDebt resets a negative balance to 0 via admin_adjust (CAS).
func (s *Service) ClearDebt(ctx context.Context, appID, userID uuid.UUID, reason, auditID string) (ClearDebtResult, error) {
	if reason == "" {
		reason = "debt cleared"
	}
	if auditID == "" {
		auditID = uuid.Must(uuid.NewV7()).String()
	}
	key := "admin:" + auditID

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var out ClearDebtResult
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			bal, err := s.store.GetBalanceOrZero(ctx, tx, appID, userID)
			if err != nil {
				return err
			}
			if bal >= 0 {
				out = ClearDebtResult{Cleared: 0, Balance: bal, LedgerEntry: nil}
				return nil
			}
			now := db.Now()
			ok, err := s.store.CASZeroBalance(ctx, tx, appID, userID, bal, now)
			if err != nil {
				return err
			}
			if !ok {
				return errRetryClear
			}
			cleared := -bal // positive credits restored
			meta, _ := json.Marshal(map[string]any{"action": "clear_debt"})
			refType := "admin"
			e := Entry{
				ID: uuid.Must(uuid.NewV7()), AppID: appID, UserID: userID,
				EntryType: TypeAdminAdjust, Amount: cleared, BalanceAfter: 0,
				IdempotencyKey: key, ReferenceType: &refType, ReferenceID: &auditID,
				Actor: "admin", Reason: reason, Metadata: meta, CreatedAt: now,
			}
			if err := s.store.InsertEntry(ctx, tx, e); err != nil {
				return err
			}
			out = ClearDebtResult{Cleared: cleared, Balance: 0, LedgerEntry: &e}
			return nil
		})
		if errors.Is(err, errRetryClear) {
			continue
		}
		if err != nil {
			return ClearDebtResult{}, err
		}
		return out, nil
	}
	return ClearDebtResult{}, ErrConcurrentMod
}

var errRetryClear = errors.New("credit: retry clear debt")

// Mismatch describes a reconcile failure for one user.
type Mismatch struct {
	AppID   uuid.UUID `json:"app_id"`
	UserID  uuid.UUID `json:"user_id"`
	Balance int64     `json:"balance"`
	Sum     int64     `json:"ledger_sum"`
	Latest  *int64    `json:"latest_balance_after,omitempty"`
	Problem string    `json:"problem"`
}

// Reconcile verifies balance == SUM(ledger) == latest balance_after for all (or one app).
func (s *Service) Reconcile(ctx context.Context, appID *uuid.UUID) ([]Mismatch, error) {
	balances, err := s.store.ListBalanceUsers(ctx, nil, appID)
	if err != nil {
		return nil, err
	}
	var out []Mismatch
	for _, b := range balances {
		sum, err := s.store.SumLedger(ctx, nil, b.AppID, b.UserID)
		if err != nil {
			return nil, err
		}
		latest, ok, err := s.store.LatestBalanceAfter(ctx, nil, b.AppID, b.UserID)
		if err != nil {
			return nil, err
		}
		m := Mismatch{AppID: b.AppID, UserID: b.UserID, Balance: b.Balance, Sum: sum}
		if ok {
			m.Latest = &latest
		}
		switch {
		case b.Balance != sum:
			m.Problem = "balance_ne_sum"
			out = append(out, m)
		case ok && b.Balance != latest:
			m.Problem = "balance_ne_latest"
			out = append(out, m)
		case !ok && b.Balance != 0:
			m.Problem = "balance_without_ledger"
			out = append(out, m)
		}
	}
	return out, nil
}
