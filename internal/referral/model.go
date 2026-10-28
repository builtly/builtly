package referral

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

const (
	StatusPending  = "pending"
	StatusRewarded = "rewarded"
	StatusVoid     = "void"
)

// Referral is a referee→referrer link.
type Referral struct {
	ID              uuid.UUID `db:"id" json:"id"`
	AppID           uuid.UUID `db:"app_id" json:"app_id"`
	ReferrerUserID  uuid.UUID `db:"referrer_user_id" json:"referrer_user_id"`
	RefereeUserID   uuid.UUID `db:"referee_user_id" json:"referee_user_id"`
	Code            string    `db:"code" json:"code"`
	Status          string    `db:"status" json:"status"`
	RewardedCredits int64     `db:"rewarded_credits" json:"rewarded_credits"`
	RewardCount     int64     `db:"reward_count" json:"reward_count"`
	VoidReason      string    `db:"void_reason" json:"void_reason,omitempty"`
	CreatedAt       db.Time   `db:"created_at" json:"created_at"`
	UpdatedAt       db.Time   `db:"updated_at" json:"updated_at"`
}

// ApplyResult is returned after applying a code (signup or explicit).
type ApplyResult struct {
	Applied      bool   `json:"applied"`
	Reason       string `json:"reason,omitempty"`
	RefereeBonus int64  `json:"referee_bonus,omitempty"`
	Balance      int64  `json:"balance,omitempty"`
	ReferralID   string `json:"referral_id,omitempty"`
}

// PurchaseInfo is the billing data referral needs (injected).
type PurchaseInfo struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Credits         int64
	RefundedCredits int64
	Environment     string
	Status          string
}

// Purchases looks up purchases without importing billing.
type Purchases interface {
	GetPurchase(ctx context.Context, q db.Querier, appID, purchaseID uuid.UUID) (PurchaseInfo, error)
	HasCompletedPurchase(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (bool, error)
}

// Users looks up users by referral code / id.
type Users interface {
	GetByReferralCode(ctx context.Context, q db.Querier, appID uuid.UUID, code string) (UserView, error)
	GetUser(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (UserView, error)
}

// UserView is the subset of user fields referral needs.
type UserView struct {
	ID           uuid.UUID
	Status       string
	ReferralCode *string
	Email        *string
	DisplayName  string
	CreatedAt    db.Time
}
