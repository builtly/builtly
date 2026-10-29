package referral

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists referrals.
type Store struct{ db *db.DB }

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectReferral = `SELECT id, app_id, referrer_user_id, referee_user_id, code, status,
	rewarded_credits, reward_count, void_reason, created_at, updated_at FROM referrals`

// Insert creates a referral row.
func (s *Store) Insert(ctx context.Context, q db.Querier, r Referral) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO referrals (
			id, app_id, referrer_user_id, referee_user_id, code, status,
			rewarded_credits, reward_count, void_reason, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.ID, r.AppID, r.ReferrerUserID, r.RefereeUserID, r.Code, r.Status,
		r.RewardedCredits, r.RewardCount, r.VoidReason, r.CreatedAt, r.UpdatedAt)
	return db.MapError(err)
}

// GetByReferee loads the referral for a referee (any status).
func (s *Store) GetByReferee(ctx context.Context, q db.Querier, appID, refereeID uuid.UUID) (Referral, error) {
	var r Referral
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectReferral+`
		WHERE app_id = ? AND referee_user_id = ?`), appID, refereeID)
	return r, db.MapError(err)
}

// GetByID loads a referral.
func (s *Store) GetByID(ctx context.Context, q db.Querier, appID, id uuid.UUID) (Referral, error) {
	var r Referral
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectReferral+`
		WHERE app_id = ? AND id = ?`), appID, id)
	return r, db.MapError(err)
}

// AddReward increments reward counters and sets status rewarded.
func (s *Store) AddReward(ctx context.Context, q db.Querier, appID, id uuid.UUID, credits int64, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE referrals SET reward_count = reward_count + 1,
		                   rewarded_credits = rewarded_credits + ?,
		                   status = ?, updated_at = ?
		WHERE app_id = ? AND id = ? AND status != ?`),
		credits, StatusRewarded, now, appID, id, StatusVoid)
	return db.MapError(err)
}

// SubtractRewardedCredits decreases rewarded_credits after a reversal.
func (s *Store) SubtractRewardedCredits(ctx context.Context, q db.Querier, appID, id uuid.UUID, delta int64, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE referrals SET rewarded_credits = rewarded_credits - ?, updated_at = ?
		WHERE app_id = ? AND id = ?`), delta, now, appID, id)
	return db.MapError(err)
}

// Void marks a referral void.
func (s *Store) Void(ctx context.Context, q db.Querier, appID, id uuid.UUID, reason string, now db.Time) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE referrals SET status = ?, void_reason = ?, updated_at = ?
		WHERE app_id = ? AND id = ? AND status != ?`),
		StatusVoid, reason, now, appID, id, StatusVoid)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// VoidByReferee voids the referral where the user is the referee.
func (s *Store) VoidByReferee(ctx context.Context, q db.Querier, appID, refereeID uuid.UUID, reason string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE referrals SET status = ?, void_reason = ?, updated_at = ?
		WHERE app_id = ? AND referee_user_id = ? AND status != ?`),
		StatusVoid, reason, now, appID, refereeID, StatusVoid)
	return db.MapError(err)
}

// Stats returns aggregate stats for a referrer.
func (s *Store) Stats(ctx context.Context, q db.Querier, appID, referrerID uuid.UUID) (referees, rewardedReferees, creditsEarned int64, err error) {
	err = s.q(q).GetContext(ctx, &referees, s.db.Rebind(`
		SELECT COUNT(*) FROM referrals WHERE app_id = ? AND referrer_user_id = ? AND status != ?`),
		appID, referrerID, StatusVoid)
	if err != nil {
		return 0, 0, 0, db.MapError(err)
	}
	err = s.q(q).GetContext(ctx, &rewardedReferees, s.db.Rebind(`
		SELECT COUNT(*) FROM referrals WHERE app_id = ? AND referrer_user_id = ? AND reward_count > 0`),
		appID, referrerID)
	if err != nil {
		return 0, 0, 0, db.MapError(err)
	}
	err = s.q(q).GetContext(ctx, &creditsEarned, s.db.Rebind(`
		SELECT COALESCE(SUM(rewarded_credits), 0) FROM referrals
		WHERE app_id = ? AND referrer_user_id = ?`), appID, referrerID)
	return referees, rewardedReferees, creditsEarned, db.MapError(err)
}

// CountRewardedReferrals counts referrer's referrals with reward_count > 0 excluding one id.
func (s *Store) CountRewardedReferrals(ctx context.Context, q db.Querier, appID, referrerID, excludeID uuid.UUID) (int64, error) {
	var n int64
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`
		SELECT COUNT(*) FROM referrals
		WHERE app_id = ? AND referrer_user_id = ? AND reward_count > 0 AND id != ?`),
		appID, referrerID, excludeID)
	return n, db.MapError(err)
}

// ListByReferrer lists referrals for a referrer (user-facing, excludes void optionally).
func (s *Store) ListByReferrer(ctx context.Context, q db.Querier, appID, referrerID uuid.UUID, limit int, after db.Time, afterID uuid.UUID) ([]Referral, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows []Referral
	var err error
	if afterID == uuid.Nil {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectReferral+`
			WHERE app_id = ? AND referrer_user_id = ?
			ORDER BY created_at DESC, id DESC LIMIT ?`), appID, referrerID, limit)
	} else {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectReferral+`
			WHERE app_id = ? AND referrer_user_id = ?
			  AND (created_at < ? OR (created_at = ? AND id < ?))
			ORDER BY created_at DESC, id DESC LIMIT ?`),
			appID, referrerID, after, after, afterID, limit)
	}
	return rows, db.MapError(err)
}

func referralAdminWhere(appID uuid.UUID, referrerID *uuid.UUID, status string) (string, []any) {
	where := `WHERE app_id = ?`
	args := []any{appID}
	if referrerID != nil {
		where += ` AND referrer_user_id = ?`
		args = append(args, *referrerID)
	}
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	return where, args
}

// CountAdmin counts referrals for an app with optional filters.
func (s *Store) CountAdmin(ctx context.Context, q db.Querier, appID uuid.UUID, referrerID *uuid.UUID, status string) (int, error) {
	where, args := referralAdminWhere(appID, referrerID, status)
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM referrals `+where), args...)
	if err != nil {
		return 0, db.MapError(err)
	}
	return n, nil
}

// ListAdmin lists referrals for an app with optional filters.
func (s *Store) ListAdmin(ctx context.Context, q db.Querier, appID uuid.UUID, referrerID *uuid.UUID, status string, limit int, after db.Time, afterID uuid.UUID) ([]Referral, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where, args := referralAdminWhere(appID, referrerID, status)
	if afterID != uuid.Nil {
		where += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, after, after, afterID)
	}
	args = append(args, limit)
	var rows []Referral
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectReferral+` `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`), args...)
	return rows, db.MapError(err)
}

// IsNotFound reports db.ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, db.ErrNotFound) }
