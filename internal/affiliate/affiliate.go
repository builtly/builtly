// Package affiliate integrates PromoteKit / Affonso attribution hooks.
package affiliate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/tenant"
)

// ErrUnsupported means the provider has no server-side conversion API for this path.
var ErrUnsupported = errors.New("affiliate: unsupported")

// Provider writes Stripe checkout fields and optional IAP conversion posts.
type Provider interface {
	CheckoutFields(affiliateRef string) map[string]string
	RecordConversion(ctx context.Context, apiKey string, conv Conversion) error
	RecordRefund(ctx context.Context, apiKey string, r Refund) error
}

// Conversion is an IAP purchase conversion.
type Conversion struct {
	AffiliateRef string
	AmountCents  int64
	Currency     string
	OrderID      string
	UserID       string
}

// Refund is an IAP refund notification.
type Refund struct {
	AffiliateRef string
	OrderID      string
	AmountCents  int64
	Currency     string
}

// PromoteKit adapter.
type PromoteKit struct {
	HTTP    *http.Client
	BaseURL string
}

func (p *PromoteKit) CheckoutFields(ref string) map[string]string {
	if ref == "" {
		return nil
	}
	return map[string]string{"metadata[promotekit_referral]": ref}
}

func (p *PromoteKit) RecordConversion(context.Context, string, Conversion) error {
	return ErrUnsupported
}
func (p *PromoteKit) RecordRefund(context.Context, string, Refund) error { return ErrUnsupported }

// Affonso adapter.
type Affonso struct {
	HTTP    *http.Client
	BaseURL string
}

func (a *Affonso) CheckoutFields(ref string) map[string]string {
	if ref == "" {
		return nil
	}
	return map[string]string{"metadata[affonso_referral]": ref}
}

func (a *Affonso) RecordConversion(context.Context, string, Conversion) error { return ErrUnsupported }
func (a *Affonso) RecordRefund(context.Context, string, Refund) error         { return ErrUnsupported }

// Registry selects a provider by name.
type Registry struct {
	PromoteKit *PromoteKit
	Affonso    *Affonso
}

// NewRegistry constructs adapters.
func NewRegistry(hc *http.Client) *Registry {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Registry{
		PromoteKit: &PromoteKit{HTTP: hc},
		Affonso:    &Affonso{HTTP: hc},
	}
}

// Get returns a provider or nil for "none".
func (r *Registry) Get(name string) Provider {
	switch strings.ToLower(name) {
	case "promotekit":
		return r.PromoteKit
	case "affonso":
		return r.Affonso
	default:
		return nil
	}
}

// CheckoutFieldsForApp returns Stripe form fields for the app's affiliate provider.
func CheckoutFieldsForApp(reg *Registry, app tenant.App, affiliateRef string) map[string]string {
	if reg == nil || affiliateRef == "" {
		return nil
	}
	p := reg.Get(app.Settings.Affiliate.Provider)
	if p == nil {
		return nil
	}
	return p.CheckoutFields(affiliateRef)
}

// Subscriber handles affiliate outbox rows (IAP conversions / refunds).
type Subscriber struct {
	reg      *Registry
	resolver *integration.Resolver
	tenants  *tenant.Service
}

// NewSubscriber constructs a Subscriber.
func NewSubscriber(reg *Registry, resolver *integration.Resolver, tenants *tenant.Service) *Subscriber {
	return &Subscriber{reg: reg, resolver: resolver, tenants: tenants}
}

// Handle implements outbox.Handler.
func (s *Subscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: fmt.Errorf("affiliate: missing app_id")}
	}
	app, err := s.tenants.Get(ctx, *row.AppID)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	provName := app.Settings.Affiliate.Provider
	if provName == "" || provName == "none" {
		return outbox.ErrNotConfigured
	}
	p := s.reg.Get(provName)
	if p == nil {
		return outbox.ErrNotConfigured
	}
	providerID := integration.ProviderPromoteKit
	if provName == "affonso" {
		providerID = integration.ProviderAffonso
	}
	resolved, err := s.resolver.For(ctx, *row.AppID, providerID)
	if err == integration.ErrNotConfigured {
		return outbox.ErrNotConfigured
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	var sec integration.APIKeySecrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil || sec.APIKey == "" {
		return outbox.ErrNotConfigured
	}

	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	switch row.EventType {
	case outbox.EventPurchaseCompleted:
		if str(m["provider"]) == "stripe" {
			return nil // Stripe attributed via Checkout metadata
		}
		err = p.RecordConversion(ctx, sec.APIKey, Conversion{
			AffiliateRef: str(m["affiliate_ref"]),
			OrderID:      str(m["purchase_id"]),
			UserID:       str(m["user_id"]),
		})
	case outbox.EventPurchaseRefunded:
		src := str(m["source"])
		if strings.HasPrefix(src, "stripe") {
			return nil
		}
		err = p.RecordRefund(ctx, sec.APIKey, Refund{OrderID: str(m["purchase_id"])})
	default:
		return nil
	}
	if errors.Is(err, ErrUnsupported) {
		return outbox.ErrNotConfigured // → skipped
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
