package referral

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
)

// Subscriber rewards / reverses on purchase outbox events.
type Subscriber struct {
	svc *Service
}

// NewSubscriber constructs a Subscriber.
func NewSubscriber(svc *Service) *Subscriber { return &Subscriber{svc: svc} }

// Handle implements outbox.Handler.
func (sub *Subscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: fmt.Errorf("referral: missing app_id")}
	}
	switch row.EventType {
	case outbox.EventPurchaseCompleted:
		return sub.onPurchaseCompleted(ctx, *row.AppID, raw)
	case outbox.EventPurchaseRefunded:
		return sub.onPurchaseRefunded(ctx, *row.AppID, raw)
	default:
		return nil
	}
}

func (sub *Subscriber) onPurchaseCompleted(ctx context.Context, appID uuid.UUID, raw []byte) error {
	var p struct {
		PurchaseID string `json:"purchase_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.PurchaseID == "" {
		return outbox.ErrPermanent{Err: fmt.Errorf("referral: bad payload")}
	}
	purchaseID, err := uuid.Parse(p.PurchaseID)
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	app, err := sub.svc.tenants.Get(ctx, appID)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	cfg := app.Settings.Referral
	if !cfg.Enabled {
		return nil
	}
	purchase, err := sub.svc.purchases.GetPurchase(ctx, nil, appID, purchaseID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return outbox.ErrRetryable{Err: err}
	}
	if purchase.Environment == "sandbox" {
		return nil
	}
	ref, err := sub.svc.store.GetByReferee(ctx, nil, appID, purchase.UserID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return outbox.ErrRetryable{Err: err}
	}
	if ref.Status == StatusVoid {
		return nil
	}
	referrer, err := sub.svc.users.GetUser(ctx, nil, appID, ref.ReferrerUserID)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	if referrer.Status != "active" {
		return nil
	}
	if cfg.Trigger == "first_purchase" && ref.RewardCount > 0 {
		return nil
	}
	if cfg.MaxRewardedReferees > 0 {
		n, err := sub.svc.store.CountRewardedReferrals(ctx, nil, appID, ref.ReferrerUserID, ref.ID)
		if err != nil {
			return outbox.ErrRetryable{Err: err}
		}
		if n >= cfg.MaxRewardedReferees {
			return nil
		}
	}
	var reward int64
	if cfg.RewardType == "percent" {
		reward = purchase.Credits * cfg.RewardValue / 100
	} else {
		reward = cfg.RewardValue
	}
	if reward <= 0 {
		return nil
	}
	key := "affiliate:purchase:" + purchaseID.String()
	if cfg.Trigger == "first_purchase" {
		key = "affiliate:first:" + ref.ID.String()
	}
	meta, _ := json.Marshal(map[string]any{
		"referral_id": ref.ID.String(), "referee_user_id": purchase.UserID.String(),
	})
	return sub.svc.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := sub.svc.credits.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: appID, UserID: ref.ReferrerUserID, EntryType: credit.TypeAffiliatePayout,
			Amount: reward, IdempotencyKey: key,
			ReferenceType: "purchase", ReferenceID: purchaseID.String(),
			Actor: "system", Reason: "referral reward", Metadata: meta,
		})
		if err != nil {
			return err
		}
		if !res.Applied {
			return nil
		}
		if err := sub.svc.store.AddReward(ctx, tx, appID, ref.ID, reward, db.Now()); err != nil {
			return err
		}
		appCopy := appID
		payload, _ := json.Marshal(map[string]any{
			"referrer_id": ref.ReferrerUserID.String(), "referee_id": purchase.UserID.String(),
			"credits": reward, "referral_id": ref.ID.String(), "purchase_id": purchaseID.String(),
		})
		return sub.svc.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appCopy, Type: outbox.EventReferralRewarded, Payload: payload,
		})
	})
}

func (sub *Subscriber) onPurchaseRefunded(ctx context.Context, appID uuid.UUID, raw []byte) error {
	var p struct {
		PurchaseID string `json:"purchase_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.PurchaseID == "" {
		return outbox.ErrPermanent{Err: fmt.Errorf("referral: bad refund payload")}
	}
	purchaseID, err := uuid.Parse(p.PurchaseID)
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	purchase, err := sub.svc.purchases.GetPurchase(ctx, nil, appID, purchaseID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return outbox.ErrRetryable{Err: err}
	}
	if purchase.Credits <= 0 {
		return nil
	}
	payout, err := sub.svc.credits.GetByReference(ctx, appID, credit.TypeAffiliatePayout, "purchase", purchaseID.String())
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return outbox.ErrRetryable{Err: err}
	}
	already, err := sub.svc.credits.SumReversals(ctx, appID, "purchase", purchaseID.String())
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	target := payout.Amount * purchase.RefundedCredits / purchase.Credits
	delta := target - already
	if delta <= 0 {
		return nil
	}
	ref, refErr := sub.svc.store.GetByReferee(ctx, nil, appID, purchase.UserID)
	key := fmt.Sprintf("affiliate_reversal:%s:%d", purchaseID.String(), target)
	return sub.svc.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := sub.svc.credits.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: appID, UserID: payout.UserID, EntryType: credit.TypeAffiliateReversal,
			Amount: -delta, IdempotencyKey: key,
			ReferenceType: "purchase", ReferenceID: purchaseID.String(),
			Actor: "system", Reason: "referral reward reversal",
		}); err != nil {
			return err
		}
		if refErr == nil {
			return sub.svc.store.SubtractRewardedCredits(ctx, tx, appID, ref.ID, delta, db.Now())
		}
		return nil
	})
}
