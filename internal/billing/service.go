package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/affiliate"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

// Service owns checkout, webhooks, and refunds.
type Service struct {
	db         *db.DB
	store      *Store
	catalog    *catalog.Service
	credits    *credit.Service
	outbox     *outbox.Publisher
	resolver   *integration.Resolver
	integ      *integration.Service
	iStore     *integration.Store
	box        *secrets.Box
	users      *auth.Store
	authTokens *auth.TokenService
	tenants    *tenant.Service
	stripe     *stripeapi.Client
	devHTTP    bool // allow http://localhost* checkout URLs
	iap        iapDeps
	// AffiliateFields merges provider-specific Stripe Checkout form fields (optional).
	AffiliateFields func(app tenant.App, affiliateRef string) map[string]string
}

// NewService constructs a billing Service.
func NewService(
	database *db.DB,
	store *Store,
	cat *catalog.Service,
	credits *credit.Service,
	pub *outbox.Publisher,
	resolver *integration.Resolver,
	integ *integration.Service,
	iStore *integration.Store,
	box *secrets.Box,
	users *auth.Store,
	tenants *tenant.Service,
	stripe *stripeapi.Client,
	devHTTP bool,
) *Service {
	return &Service{
		db: database, store: store, catalog: cat, credits: credits, outbox: pub,
		resolver: resolver, integ: integ, iStore: iStore, box: box, users: users,
		tenants: tenants, stripe: stripe, devHTTP: devHTTP,
	}
}

// Stripe returns the Stripe API client (for admin connection enrichment).
func (s *Service) Stripe() *stripeapi.Client { return s.stripe }

// SetAuthTokens wires the auth TokenService for validating guest access tokens during transfer.
func (s *Service) SetAuthTokens(tokens *auth.TokenService) {
	s.authTokens = tokens
}

// Store exposes the purchase store for admin listing.
func (s *Service) Store() *Store { return s.store }

func (s *Service) publishGuestTransfer(ctx context.Context, tx *sqlx.Tx, appID, formalUserID, guestUserID uuid.UUID, credits int64) error {
	if s.outbox == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"user_id":       formalUserID.String(),
		"guest_user_id": guestUserID.String(),
		"credits":       credits,
	})
	if err != nil {
		return err
	}
	return s.outbox.PublishTx(ctx, tx, outbox.Event{
		AppID: &appID, Type: outbox.EventGuestCreditsTransferred, Payload: payload,
	})
}

// publishAnalytics records a fact after the business write has committed.
// A missing analytics key is skipped later by the subscriber; a publish failure must not fail the user action.
func (s *Service) publishAnalytics(ctx context.Context, appID uuid.UUID, eventType string, fields map[string]any) {
	if s.outbox == nil || s.db == nil {
		return
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return
	}
	appCopy := appID
	if err := s.outbox.Publish(ctx, s.db, outbox.Event{
		AppID: &appCopy, Type: eventType, Payload: payload,
	}); err != nil {
		slog.Warn("analytics publish failed", "event", eventType, "err", err)
	}
}

// SetAffiliate wires PromoteKit/Affonso checkout field helpers.
func (s *Service) SetAffiliate(reg *affiliate.Registry) {
	s.AffiliateFields = func(app tenant.App, ref string) map[string]string {
		return affiliate.CheckoutFieldsForApp(reg, app, ref)
	}
}

// applyRefund claws back credits to reach targetRefundedCredits (architecture §3.7).
func (s *Service) applyRefund(ctx context.Context, tx *sqlx.Tx, p Purchase, target int64, key, source string) error {
	if target < 0 {
		target = 0
	}
	if target > p.Credits {
		target = p.Credits
	}
	delta := target - p.RefundedCredits
	if delta <= 0 {
		return nil
	}
	meta, _ := json.Marshal(map[string]any{"source": source, "target_refunded_credits": target})
	if _, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
		AppID: p.AppID, UserID: p.UserID, EntryType: credit.TypeRefund,
		Amount: -delta, IdempotencyKey: key,
		ReferenceType: "purchase", ReferenceID: p.ID.String(),
		Actor: "system", Reason: source, Metadata: meta,
	}); err != nil {
		return err
	}
	status := StatusPartialRefunded
	if target == p.Credits {
		status = StatusRefunded
	}
	if err := s.store.SetRefundedCredits(ctx, tx, p.AppID, p.ID, target, status, db.Now()); err != nil {
		return err
	}
	appID := p.AppID
	payload, _ := json.Marshal(map[string]any{
		"purchase_id": p.ID.String(), "user_id": p.UserID.String(),
		"delta": delta, "full": target == p.Credits,
		"refunded_credits": target, "source": source,
	})
	return s.outbox.PublishTx(ctx, tx, outbox.Event{
		AppID: &appID, Type: outbox.EventPurchaseRefunded, Payload: payload,
	})
}

// applyRefundReversal restores credits after a dispute won (or similar).
func (s *Service) applyRefundReversal(ctx context.Context, tx *sqlx.Tx, p Purchase, restore int64, key, source string) error {
	if restore <= 0 {
		return nil
	}
	if restore > p.RefundedCredits {
		restore = p.RefundedCredits
	}
	meta, _ := json.Marshal(map[string]any{"source": source})
	if _, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
		AppID: p.AppID, UserID: p.UserID, EntryType: credit.TypeRefundReversal,
		Amount: restore, IdempotencyKey: key,
		ReferenceType: "purchase", ReferenceID: p.ID.String(),
		Actor: "system", Reason: source, Metadata: meta,
	}); err != nil {
		return err
	}
	newRefunded := p.RefundedCredits - restore
	status := StatusCompleted
	if newRefunded > 0 && newRefunded < p.Credits {
		status = StatusPartialRefunded
	} else if newRefunded >= p.Credits {
		status = StatusRefunded
	}
	return s.store.SetRefundedCredits(ctx, tx, p.AppID, p.ID, newRefunded, status, db.Now())
}

// fulfilCheckout marks purchase completed and grants credits.
func (s *Service) fulfilCheckout(ctx context.Context, tx *sqlx.Tx, appID uuid.UUID, sessionID string, amountTotal int64, currency, paymentIntent, purchaseIDMeta string) error {
	p, err := s.store.GetPurchaseByProviderRef(ctx, tx, appID, ProviderStripe, sessionID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("purchase not found for session %s", sessionID)
		}
		return err
	}
	if purchaseIDMeta != "" && purchaseIDMeta != p.ID.String() {
		return fmt.Errorf("purchase_id metadata mismatch")
	}
	if p.Status == StatusCompleted || p.Status == StatusPartialRefunded || p.Status == StatusRefunded {
		return nil // already fulfilled
	}
	if p.Status != StatusPending {
		return fmt.Errorf("purchase status %s", p.Status)
	}
	now := db.Now()
	ok, err := s.store.MarkPurchaseCompleted(ctx, tx, appID, p.ID, amountTotal, currency, paymentIntent, now)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	key := "stripe:checkout:" + sessionID
	if _, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
		AppID: appID, UserID: p.UserID, EntryType: credit.TypeStripePurchase,
		Amount: p.Credits, IdempotencyKey: key,
		ReferenceType: "purchase", ReferenceID: p.ID.String(),
		Actor: "system", Reason: "stripe checkout",
	}); err != nil {
		return err
	}
	var sku string
	if s.catalog != nil {
		if prod, err := s.catalog.GetTx(ctx, tx, appID, p.ProductID); err == nil {
			sku = prod.SKU
		}
	}
	var affRef string
	if p.AffiliateRef != nil {
		affRef = *p.AffiliateRef
	}
	appIDCopy := appID
	payload, _ := json.Marshal(map[string]any{
		"purchase_id":   p.ID.String(),
		"user_id":       p.UserID.String(),
		"product_id":    p.ProductID.String(),
		"product_sku":   sku,
		"credits":       p.Credits,
		"provider":      ProviderStripe,
		"amount":        amountTotal,
		"currency":      currency,
		"environment":   p.Environment,
		"affiliate_ref": affRef,
	})
	return s.outbox.PublishTx(ctx, tx, outbox.Event{
		AppID: &appIDCopy, Type: outbox.EventPurchaseCompleted, Payload: payload,
	})
}
