package referral

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// Service owns apply / query / void.
type Service struct {
	db        *db.DB
	store     *Store
	credits   *credit.Service
	outbox    *outbox.Publisher
	users     Users
	purchases Purchases
	tenants   *tenant.Service
}

// NewService constructs a referral Service.
func NewService(
	database *db.DB,
	store *Store,
	credits *credit.Service,
	pub *outbox.Publisher,
	users Users,
	purchases Purchases,
	tenants *tenant.Service,
) *Service {
	return &Service{
		db: database, store: store, credits: credits, outbox: pub,
		users: users, purchases: purchases, tenants: tenants,
	}
}

// Store exposes the referral store (auth delete hook).
func (s *Service) Store() *Store { return s.store }

// ApplyTx applies a referral code inside an existing transaction. Soft failures return ApplyResult without error.
func (s *Service) ApplyTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, refereeID uuid.UUID, code string) (ApplyResult, error) {
	code = NormalizeCode(code)
	if code == "" {
		return ApplyResult{Applied: false, Reason: "referral_code_invalid"}, nil
	}
	if !app.Settings.Referral.Enabled {
		return ApplyResult{Applied: false, Reason: "referral_disabled"}, nil
	}
	if _, err := s.store.GetByReferee(ctx, tx, app.ID, refereeID); err == nil {
		return ApplyResult{Applied: false, Reason: "referral_already_applied"}, nil
	} else if !IsNotFound(err) {
		return ApplyResult{}, err
	}
	referrer, err := s.users.GetByReferralCode(ctx, tx, app.ID, code)
	if err != nil {
		if IsNotFound(err) || errors.Is(err, db.ErrNotFound) {
			return ApplyResult{Applied: false, Reason: "referral_code_invalid"}, nil
		}
		return ApplyResult{}, err
	}
	if referrer.Status != "active" {
		return ApplyResult{Applied: false, Reason: "referral_code_invalid"}, nil
	}
	if referrer.ID == refereeID {
		return ApplyResult{Applied: false, Reason: "referral_self"}, nil
	}
	referee, err := s.users.GetUser(ctx, tx, app.ID, refereeID)
	if err != nil {
		return ApplyResult{}, err
	}
	window := app.Settings.Referral.ApplyWindowDays
	if window > 0 {
		deadline := time.Time(referee.CreatedAt).UTC().Add(time.Duration(window) * 24 * time.Hour)
		if time.Now().UTC().After(deadline) {
			return ApplyResult{Applied: false, Reason: "referral_window_closed"}, nil
		}
	}
	if s.purchases != nil {
		has, err := s.purchases.HasCompletedPurchase(ctx, tx, app.ID, refereeID)
		if err != nil {
			return ApplyResult{}, err
		}
		if has {
			return ApplyResult{Applied: false, Reason: "referral_window_closed"}, nil
		}
	}

	now := db.Now()
	refID := uuid.Must(uuid.NewV7())
	r := Referral{
		ID: refID, AppID: app.ID, ReferrerUserID: referrer.ID, RefereeUserID: refereeID,
		Code: code, Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.Insert(ctx, tx, r); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			return ApplyResult{Applied: false, Reason: "referral_already_applied"}, nil
		}
		return ApplyResult{}, err
	}

	out := ApplyResult{Applied: true, ReferralID: refID.String()}
	bonus := app.Settings.Referral.RefereeBonusCredits
	if bonus > 0 {
		res, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: app.ID, UserID: refereeID, EntryType: credit.TypeReferralBonus,
			Amount: bonus, IdempotencyKey: "referral_bonus:" + refID.String(),
			ReferenceType: "referral", ReferenceID: refID.String(),
			Actor: "system", Reason: "referral referee bonus",
		})
		if err != nil {
			return ApplyResult{}, err
		}
		out.RefereeBonus = bonus
		out.Balance = res.Balance
	}
	return out, nil
}

// Apply is the explicit POST /user/referral/apply path (hard errors).
func (s *Service) Apply(ctx context.Context, app tenant.App, refereeID uuid.UUID, code string) (ApplyResult, error) {
	var out ApplyResult
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var err error
		out, err = s.ApplyTx(ctx, tx, app, refereeID, code)
		return err
	})
	if err != nil {
		return ApplyResult{}, err
	}
	if !out.Applied {
		return ApplyResult{}, mapApplyReason(out.Reason)
	}
	if out.Balance == 0 && out.RefereeBonus == 0 {
		bal, _ := s.credits.GetBalance(ctx, app.ID, refereeID)
		out.Balance = bal.Balance
	}
	return out, nil
}

func mapApplyReason(reason string) error {
	switch reason {
	case "referral_disabled":
		return ErrDisabled
	case "referral_code_invalid":
		return ErrCodeInvalid
	case "referral_self":
		return ErrSelf
	case "referral_already_applied":
		return ErrAlreadyApplied
	case "referral_window_closed":
		return ErrWindowClosed
	default:
		return ErrCodeInvalid
	}
}

// GetSummary is GET /user/referral.
func (s *Service) GetSummary(ctx context.Context, app tenant.App, userID uuid.UUID) (map[string]any, error) {
	u, err := s.users.GetUser(ctx, nil, app.ID, userID)
	if err != nil {
		return nil, err
	}
	code := ""
	if u.ReferralCode != nil {
		code = *u.ReferralCode
	}
	enabled := app.Settings.Referral.Enabled
	_, referredErr := s.store.GetByReferee(ctx, nil, app.ID, userID)
	referredBy := referredErr == nil
	out := map[string]any{
		"enabled": enabled, "code": code, "referred_by": referredBy,
		"stats": map[string]any{"referees": 0, "rewarded_referees": 0, "credits_earned": 0},
	}
	if !enabled {
		return out, nil
	}
	refs, rewarded, earned, err := s.store.Stats(ctx, nil, app.ID, userID)
	if err != nil {
		return nil, err
	}
	out["stats"] = map[string]any{
		"referees": refs, "rewarded_referees": rewarded, "credits_earned": earned,
	}
	return out, nil
}

// ListReferees is GET /user/referral/referees (privacy-preserving).
func (s *Service) ListReferees(ctx context.Context, appID, userID uuid.UUID, limit int, cursor string) ([]map[string]any, string, error) {
	after, afterID, err := parseCursor(cursor)
	if err != nil {
		return nil, "", ErrValidation
	}
	rows, err := s.store.ListByReferrer(ctx, nil, appID, userID, limit, after, afterID)
	if err != nil {
		return nil, "", err
	}
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		u, err := s.users.GetUser(ctx, nil, appID, r.RefereeUserID)
		if err != nil {
			continue
		}
		label := u.DisplayName
		if label == "" && u.Email != nil {
			label = maskEmail(*u.Email)
		}
		if label == "" {
			label = "user"
		}
		items = append(items, map[string]any{
			"display_name": label, "status": r.Status,
			"rewarded_credits": r.RewardedCredits, "created_at": r.CreatedAt,
		})
	}
	next := ""
	if len(rows) == limit && limit > 0 {
		last := rows[len(rows)-1]
		next = time.Time(last.CreatedAt).UTC().Format(time.RFC3339Nano) + "," + last.ID.String()
	}
	return items, next, nil
}

// VoidAdmin voids a referral.
func (s *Service) VoidAdmin(ctx context.Context, appID, referralID uuid.UUID, reason string) (Referral, error) {
	if reason == "" {
		reason = "admin_void"
	}
	now := db.Now()
	if err := s.store.Void(ctx, nil, appID, referralID, reason, now); err != nil {
		if IsNotFound(err) {
			return Referral{}, ErrNotFound
		}
		return Referral{}, err
	}
	return s.store.GetByID(ctx, nil, appID, referralID)
}

// ListAdmin lists referrals for admin.
func (s *Service) ListAdmin(ctx context.Context, appID uuid.UUID, referrerID *uuid.UUID, status string, limit int, cursor string) ([]Referral, string, error) {
	after, afterID, err := parseCursor(cursor)
	if err != nil {
		return nil, "", ErrValidation
	}
	rows, err := s.store.ListAdmin(ctx, nil, appID, referrerID, status, limit, after, afterID)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == limit && limit > 0 {
		last := rows[len(rows)-1]
		next = time.Time(last.CreatedAt).UTC().Format(time.RFC3339Nano) + "," + last.ID.String()
	}
	return rows, next, nil
}

// VoidAsReferee is called on account deletion.
func (s *Service) VoidAsReferee(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error {
	return s.store.VoidByReferee(ctx, tx, appID, userID, "referee_deleted", db.Now())
}

func parseCursor(cursor string) (db.Time, uuid.UUID, error) {
	if cursor == "" {
		return db.Time{}, uuid.Nil, nil
	}
	parts := strings.SplitN(cursor, ",", 2)
	if len(parts) != 2 {
		return db.Time{}, uuid.Nil, ErrValidation
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return db.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return db.Time{}, uuid.Nil, err
	}
	return db.Time(t), id, nil
}

func maskEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	if len(local) == 1 {
		return local + "***" + domain
	}
	return string(local[0]) + "***" + domain
}
