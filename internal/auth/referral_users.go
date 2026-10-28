package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/referral"
)

// ReferralUsers adapts Store to referral.Users.
type ReferralUsers struct{ Store *Store }

// GetByReferralCode implements referral.Users.
func (a ReferralUsers) GetByReferralCode(ctx context.Context, q db.Querier, appID uuid.UUID, code string) (referral.UserView, error) {
	u, err := a.Store.GetByReferralCode(ctx, q, appID, code)
	if err != nil {
		return referral.UserView{}, err
	}
	return toReferralUser(u), nil
}

// GetUser implements referral.Users.
func (a ReferralUsers) GetUser(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (referral.UserView, error) {
	u, err := a.Store.GetByID(ctx, q, appID, userID)
	if err != nil {
		return referral.UserView{}, err
	}
	return toReferralUser(u), nil
}

func toReferralUser(u User) referral.UserView {
	return referral.UserView{
		ID: u.ID, Status: u.Status, ReferralCode: u.ReferralCode,
		Email: u.Email, DisplayName: u.DisplayName, CreatedAt: u.CreatedAt,
	}
}
