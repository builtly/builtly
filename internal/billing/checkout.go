package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// CheckoutInput is POST /checkout/stripe.
type CheckoutInput struct {
	ProductID    uuid.UUID
	SuccessURL   string
	CancelURL    string
	AffiliateRef string
}

// CheckoutResult is the checkout session response.
type CheckoutResult struct {
	PurchaseID  uuid.UUID `json:"purchase_id"`
	SessionID   string    `json:"session_id"`
	CheckoutURL string    `json:"checkout_url"`
}

// CreateStripeCheckout creates a pending purchase and Stripe Checkout Session.
func (s *Service) CreateStripeCheckout(ctx context.Context, app tenant.App, userID uuid.UUID, in CheckoutInput) (CheckoutResult, error) {
	if err := validateCheckoutURLs(app, in.SuccessURL, in.CancelURL, s.devHTTP); err != nil {
		return CheckoutResult{}, err
	}
	product, err := s.catalog.Get(ctx, app.ID, in.ProductID)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !product.Active || product.StripePriceID == nil || *product.StripePriceID == "" {
		return CheckoutResult{}, ErrProductNotAvailable
	}
	resolved, err := s.resolver.For(ctx, app.ID, integration.ProviderStripe)
	if err == integration.ErrNotConfigured {
		return CheckoutResult{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	var sec integration.StripeSecrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil || sec.SecretKey == "" {
		return CheckoutResult{}, ErrIntegrationNotConfigured
	}
	var settings map[string]any
	_ = json.Unmarshal(resolved.Settings, &settings)
	livemode, _ := settings["livemode"].(bool)
	env := EnvSandbox
	if livemode {
		env = EnvProduction
	}

	user, err := s.users.GetByID(ctx, nil, app.ID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return CheckoutResult{}, auth.ErrUserNotFound
		}
		return CheckoutResult{}, err
	}
	affiliate := in.AffiliateRef
	if affiliate == "" {
		affiliate = attributionAffiliate(user.Attribution)
	}

	now := db.Now()
	purchaseID := uuid.Must(uuid.NewV7())
	var affPtr *string
	if affiliate != "" {
		affPtr = &affiliate
	}
	quote := product.PriceAmount
	quoteCurrency := normalizeCurrency(product.Currency)
	p := Purchase{
		ID: purchaseID, AppID: app.ID, UserID: userID, ProductID: product.ID,
		Provider: ProviderStripe, ConnectionID: resolved.ConnectionID,
		Status: StatusPending, Credits: product.Credits, Environment: env,
		Amount: &quote, Currency: &quoteCurrency,
		AffiliateRef: affPtr, ProviderData: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertPurchase(ctx, nil, p); err != nil {
		return CheckoutResult{}, err
	}

	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("line_items[0][price]", *product.StripePriceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", in.SuccessURL)
	form.Set("cancel_url", in.CancelURL)
	form.Set("client_reference_id", purchaseID.String())
	if user.Email != nil && *user.Email != "" {
		form.Set("customer_email", *user.Email)
	}
	form.Set("metadata[app_id]", app.ID.String())
	form.Set("metadata[user_id]", userID.String())
	form.Set("metadata[purchase_id]", purchaseID.String())
	form.Set("metadata[product_id]", product.ID.String())
	form.Set("payment_intent_data[metadata][app_id]", app.ID.String())
	form.Set("payment_intent_data[metadata][purchase_id]", purchaseID.String())
	if affiliate != "" {
		form.Set("metadata[affiliate_ref]", affiliate)
		form.Set("payment_intent_data[metadata][affiliate_ref]", affiliate)
		if s.AffiliateFields != nil {
			for k, v := range s.AffiliateFields(app, affiliate) {
				form.Set(k, v)
			}
		}
	}

	session, err := s.stripe.CreateCheckoutSession(ctx, sec.SecretKey, form, purchaseID.String())
	if err != nil {
		_ = s.store.MarkPurchaseStatus(ctx, nil, app.ID, purchaseID, StatusPending, StatusFailed, db.Now())
		return CheckoutResult{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	var sessionAmount *int64
	var sessionCurrency *string
	if session.AmountTotal != nil && strings.TrimSpace(session.Currency) != "" {
		sessionAmount = session.AmountTotal
		c := normalizeCurrency(session.Currency)
		sessionCurrency = &c
		quote = *session.AmountTotal
		quoteCurrency = c
	}
	if err := s.store.UpdatePurchaseAfterSession(ctx, nil, app.ID, purchaseID, session.ID, StatusPending, sessionAmount, sessionCurrency, db.Now()); err != nil {
		return CheckoutResult{}, err
	}
	s.publishAnalytics(ctx, app.ID, outbox.EventCheckoutSessionCreated, map[string]any{
		"user_id": userID.String(), "product_id": product.ID.String(), "product_sku": product.SKU,
		"credits": product.Credits, "amount": quote, "currency": quoteCurrency,
		"environment": env,
	})
	return CheckoutResult{PurchaseID: purchaseID, SessionID: session.ID, CheckoutURL: session.URL}, nil
}

func validateCheckoutURLs(app tenant.App, success, cancel string, allowHTTP bool) error {
	for _, raw := range []string{success, cancel} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ErrValidationFailed.WithDetails(map[string]any{"reason": "invalid url"})
		}
		switch u.Scheme {
		case "https":
		case "http":
			if !allowHTTP || !strings.HasPrefix(u.Host, "localhost") {
				return ErrValidationFailed.WithDetails(map[string]any{"reason": "http only allowed for localhost in development"})
			}
		default:
			return ErrValidationFailed.WithDetails(map[string]any{"reason": "url scheme must be https"})
		}
		origin := u.Scheme + "://" + u.Host
		ok := false
		for _, o := range app.Settings.AllowedOrigins {
			if o == origin {
				ok = true
				break
			}
		}
		if !ok {
			return ErrValidationFailed.WithDetails(map[string]any{"reason": "url origin not in allowed_origins"})
		}
	}
	return nil
}

func attributionAffiliate(attr []byte) string {
	if len(attr) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(attr, &m) != nil {
		return ""
	}
	s, _ := m["affiliate_ref"].(string)
	return s
}
