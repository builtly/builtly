package billing

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/referral"
)

// ReferralPurchases adapts Store to referral.Purchases.
type ReferralPurchases struct{ Store *Store }

// GetPurchase implements referral.Purchases.
func (a ReferralPurchases) GetPurchase(ctx context.Context, q db.Querier, appID, purchaseID uuid.UUID) (referral.PurchaseInfo, error) {
	p, err := a.Store.GetPurchaseByID(ctx, q, appID, purchaseID)
	if err != nil {
		return referral.PurchaseInfo{}, err
	}
	return referral.PurchaseInfo{
		ID: p.ID, UserID: p.UserID, Credits: p.Credits, RefundedCredits: p.RefundedCredits,
		Environment: p.Environment, Status: p.Status,
	}, nil
}

// HasCompletedPurchase implements referral.Purchases.
func (a ReferralPurchases) HasCompletedPurchase(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (bool, error) {
	return a.Store.HasCompletedPurchase(ctx, q, appID, userID)
}
