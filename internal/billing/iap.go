package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/billing/appstore"
	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// IAP clients (set via SetIAP).
type iapDeps struct {
	apple  *appstore.Client
	google *playstore.Client
	jobs   *JobStore
	log    *slog.Logger
}

// SetIAP wires Apple/Google clients and job store.
func (s *Service) SetIAP(apple *appstore.Client, google *playstore.Client, jobs *JobStore, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.iap = iapDeps{apple: apple, google: google, jobs: jobs, log: log}
}

// VerifyIAPInput is POST /billing/verify-iap.
type VerifyIAPInput struct {
	Platform      string // apple | google
	TransactionID string // apple
	ProductID     string // google store product id
	PurchaseToken string // google
}

// VerifyIAPResult is the verify-iap response.
type VerifyIAPResult struct {
	PurchaseID       uuid.UUID `json:"purchase_id"`
	ProductID        uuid.UUID `json:"product_id"`
	CreditsAdded     int64     `json:"credits_added"`
	Balance          int64     `json:"balance"`
	AlreadyProcessed bool      `json:"already_processed"`
}

// VerifyIAP verifies an Apple or Google IAP and grants credits.
func (s *Service) VerifyIAP(ctx context.Context, app tenant.App, userID uuid.UUID, in VerifyIAPInput) (VerifyIAPResult, error) {
	switch strings.ToLower(in.Platform) {
	case "apple":
		return s.verifyApple(ctx, app, userID, in.TransactionID)
	case "google":
		return s.verifyGoogle(ctx, app, userID, in.ProductID, in.PurchaseToken)
	default:
		return VerifyIAPResult{}, ErrValidationFailed.WithDetails(map[string]any{"reason": "platform must be apple or google"})
	}
}

func (s *Service) verifyApple(ctx context.Context, app tenant.App, userID uuid.UUID, transactionID string) (VerifyIAPResult, error) {
	if transactionID == "" {
		return VerifyIAPResult{}, ErrValidationFailed.WithDetails(map[string]any{"reason": "transaction_id required"})
	}
	if existing, err := s.store.GetPurchaseByProviderRef(ctx, nil, app.ID, ProviderApple, transactionID); err == nil {
		if existing.Status == StatusCompleted || existing.Status == StatusPartialRefunded || existing.Status == StatusRefunded {
			if existing.UserID != userID {
				return VerifyIAPResult{}, ErrPurchaseAlreadyClaimed
			}
			bal, _ := s.credits.GetBalance(ctx, app.ID, userID)
			return VerifyIAPResult{
				PurchaseID: existing.ID, ProductID: existing.ProductID,
				CreditsAdded: existing.Credits, Balance: bal.Balance, AlreadyProcessed: true,
			}, nil
		}
	} else if !errors.Is(err, db.ErrNotFound) {
		return VerifyIAPResult{}, err
	}

	cfg, err := s.appleConfig(ctx, app.ID)
	if err != nil {
		return VerifyIAPResult{}, err
	}
	tx, err := s.iap.apple.GetTransaction(ctx, cfg.Config, transactionID, app.Settings.AllowIAPSandbox)
	if err != nil {
		var ae *appstore.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return VerifyIAPResult{}, ErrReceiptInvalid.WithDetails(map[string]any{"reason": "not_found"})
		}
		return VerifyIAPResult{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	if reason := validateAppleTx(tx, cfg.BundleID, app.Settings.AllowIAPSandbox, userID); reason != "" {
		return VerifyIAPResult{}, ErrReceiptInvalid.WithDetails(map[string]any{"reason": reason})
	}
	product, err := s.catalog.GetByAppleProductID(ctx, app.ID, tx.ProductID)
	if err != nil {
		return VerifyIAPResult{}, err
	}
	env := EnvProduction
	if strings.EqualFold(tx.Environment, "Sandbox") {
		env = EnvSandbox
	}
	connID := cfg.connectionID
	return s.commitIAP(ctx, app.ID, userID, product, ProviderApple, connID, transactionID, transactionID, env, nil)
}

func (s *Service) verifyGoogle(ctx context.Context, app tenant.App, userID uuid.UUID, productID, token string) (VerifyIAPResult, error) {
	if productID == "" || token == "" {
		return VerifyIAPResult{}, ErrValidationFailed.WithDetails(map[string]any{"reason": "product_id and purchase_token required"})
	}
	ref := googleProviderRef(token)
	if existing, err := s.store.GetPurchaseByProviderRef(ctx, nil, app.ID, ProviderGoogle, ref); err == nil {
		if existing.Status == StatusCompleted || existing.Status == StatusPartialRefunded || existing.Status == StatusRefunded {
			if existing.UserID != userID {
				return VerifyIAPResult{}, ErrPurchaseAlreadyClaimed
			}
			bal, _ := s.credits.GetBalance(ctx, app.ID, userID)
			return VerifyIAPResult{
				PurchaseID: existing.ID, ProductID: existing.ProductID,
				CreditsAdded: existing.Credits, Balance: bal.Balance, AlreadyProcessed: true,
			}, nil
		}
	} else if !errors.Is(err, db.ErrNotFound) {
		return VerifyIAPResult{}, err
	}

	cfg, err := s.googleConfig(ctx, app.ID)
	if err != nil {
		return VerifyIAPResult{}, err
	}
	purchase, err := s.iap.google.GetProductPurchase(ctx, cfg.cfg, productID, token)
	if err != nil {
		var ae *playstore.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return VerifyIAPResult{}, ErrReceiptInvalid.WithDetails(map[string]any{"reason": "not_found"})
		}
		return VerifyIAPResult{}, ErrUpstream.WithDetails(map[string]any{"reason": err.Error()})
	}
	if reason := validateGooglePurchase(purchase, app.Settings.AllowIAPSandbox, userID); reason != "" {
		return VerifyIAPResult{}, ErrReceiptInvalid.WithDetails(map[string]any{"reason": reason})
	}
	// Already consumed on Google without our row
	if purchase.ConsumptionState == 1 {
		if _, err := s.store.GetPurchaseByProviderRef(ctx, nil, app.ID, ProviderGoogle, ref); errors.Is(err, db.ErrNotFound) {
			return VerifyIAPResult{}, ErrReceiptInvalid.WithDetails(map[string]any{"reason": "already_consumed"})
		}
	}
	product, err := s.catalog.GetByGoogleProductID(ctx, app.ID, productID)
	if err != nil {
		return VerifyIAPResult{}, err
	}
	env := EnvProduction
	if purchase.PurchaseType != nil && *purchase.PurchaseType == 0 {
		env = EnvSandbox
	}
	result, err := s.commitIAP(ctx, app.ID, userID, product, ProviderGoogle, cfg.connectionID, ref, purchase.OrderID, env, map[string]any{
		"purchase_token": token, "store_product_id": productID,
	})
	if err != nil {
		return VerifyIAPResult{}, err
	}
	// Consume after commit (best-effort retries).
	go s.consumeGoogle(cfg.cfg, productID, token)
	return result, nil
}

func (s *Service) consumeGoogle(cfg playstore.Config, productID, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var last error
	for i := 0; i < 3; i++ {
		if err := s.iap.google.Consume(ctx, cfg, productID, token); err == nil {
			return
		} else {
			last = err
			time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
		}
	}
	s.iap.log.Error("google consume failed", "err", last, "product_id", productID)
}

func (s *Service) commitIAP(ctx context.Context, appID, userID uuid.UUID, product catalog.Product, provider, connID, providerRef, paymentRef, env string, extra map[string]any) (VerifyIAPResult, error) {
	now := db.Now()
	purchaseID := uuid.Must(uuid.NewV7())
	var pref, pay *string
	pref, pay = &providerRef, nil
	if paymentRef != "" {
		pay = &paymentRef
	}
	data := []byte(`{}`)
	if extra != nil {
		data, _ = json.Marshal(extra)
	}
	p := Purchase{
		ID: purchaseID, AppID: appID, UserID: userID, ProductID: product.ID,
		Provider: provider, ConnectionID: connID, ProviderRef: pref, PaymentRef: pay,
		Status: StatusCompleted, Credits: product.Credits, Environment: env,
		ProviderData: data, CreatedAt: now, UpdatedAt: now,
		CompletedAt: db.NullTime{Time: now, Valid: true},
	}
	var balance int64
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := s.store.InsertPurchase(ctx, tx, p); err != nil {
			var uv db.ErrUniqueViolation
			if errors.As(err, &uv) {
				return errUniqueIAP
			}
			return err
		}
		key := iapGrantKey(provider, providerRef)
		res, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: appID, UserID: userID, EntryType: credit.TypeIAPPurchase,
			Amount: product.Credits, IdempotencyKey: key,
			ReferenceType: "purchase", ReferenceID: purchaseID.String(),
			Actor: "system", Reason: provider + " iap",
		})
		if err != nil {
			return err
		}
		balance = res.Balance
		appCopy := appID
		payload, _ := json.Marshal(map[string]any{
			"purchase_id": purchaseID.String(), "user_id": userID.String(),
			"product_id": product.ID.String(), "credits": product.Credits, "provider": provider,
		})
		return s.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appCopy, Type: outbox.EventPurchaseCompleted, Payload: payload,
		})
	})
	if errors.Is(err, errUniqueIAP) {
		// race → re-read
		existing, err2 := s.store.GetPurchaseByProviderRef(ctx, nil, appID, provider, providerRef)
		if err2 != nil {
			return VerifyIAPResult{}, err2
		}
		if existing.UserID != userID {
			return VerifyIAPResult{}, ErrPurchaseAlreadyClaimed
		}
		bal, _ := s.credits.GetBalance(ctx, appID, userID)
		return VerifyIAPResult{
			PurchaseID: existing.ID, ProductID: existing.ProductID,
			CreditsAdded: existing.Credits, Balance: bal.Balance, AlreadyProcessed: true,
		}, nil
	}
	if err != nil {
		return VerifyIAPResult{}, err
	}
	return VerifyIAPResult{
		PurchaseID: purchaseID, ProductID: product.ID,
		CreditsAdded: product.Credits, Balance: balance,
	}, nil
}

var errUniqueIAP = errors.New("billing: iap unique")

func iapGrantKey(provider, ref string) string {
	switch provider {
	case ProviderApple:
		return "iap:apple:" + ref
	case ProviderGoogle:
		return "iap:google:" + ref
	default:
		return "iap:" + provider + ":" + ref
	}
}

func googleProviderRef(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validateAppleTx(tx appstore.Transaction, bundleID string, allowSandbox bool, userID uuid.UUID) string {
	if tx.BundleID != bundleID {
		return "wrong_app"
	}
	if tx.Type != "Consumable" {
		return "not_consumable"
	}
	if tx.RevocationDate != nil {
		return "revoked"
	}
	if strings.EqualFold(tx.Environment, "Sandbox") && !allowSandbox {
		return "sandbox_not_allowed"
	}
	if tx.AppAccountToken != "" && !strings.EqualFold(tx.AppAccountToken, userID.String()) {
		return "account_mismatch"
	}
	return ""
}

func validateGooglePurchase(p playstore.ProductPurchase, allowSandbox bool, userID uuid.UUID) string {
	if p.PurchaseState != 0 {
		return "not_found"
	}
	if p.PurchaseType != nil && *p.PurchaseType == 0 && !allowSandbox {
		return "sandbox_not_allowed"
	}
	if p.ObfuscatedExternalAccountID != "" && p.ObfuscatedExternalAccountID != userID.String() {
		return "account_mismatch"
	}
	return ""
}

type appleConnCfg struct {
	appstore.Config
	connectionID string
}

type googleConnCfg struct {
	cfg          playstore.Config
	connectionID string
}

func (s *Service) appleConfig(ctx context.Context, appID uuid.UUID) (appleConnCfg, error) {
	resolved, err := s.resolver.For(ctx, appID, integration.ProviderAppleIAP)
	if err == integration.ErrNotConfigured {
		return appleConnCfg{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return appleConnCfg{}, err
	}
	var settings map[string]any
	_ = json.Unmarshal(resolved.Settings, &settings)
	var sec integration.PEMSecrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil {
		return appleConnCfg{}, ErrIntegrationNotConfigured
	}
	return appleConnCfg{
		Config: appstore.Config{
			IssuerID: str(settings["issuer_id"]), KeyID: str(settings["key_id"]),
			BundleID: str(settings["bundle_id"]), PrivateKeyPEM: sec.PrivateKeyPEM,
		},
		connectionID: resolved.ConnectionID,
	}, nil
}

func (s *Service) googleConfig(ctx context.Context, appID uuid.UUID) (googleConnCfg, error) {
	resolved, err := s.resolver.For(ctx, appID, integration.ProviderGooglePlay)
	if err == integration.ErrNotConfigured {
		return googleConnCfg{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return googleConnCfg{}, err
	}
	var settings map[string]any
	_ = json.Unmarshal(resolved.Settings, &settings)
	var sec integration.GooglePlaySecrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil {
		return googleConnCfg{}, ErrIntegrationNotConfigured
	}
	return googleConnCfg{
		cfg: playstore.Config{
			PackageName: str(settings["package_name"]), ServiceAccountJSON: []byte(sec.ServiceAccountJSON),
		},
		connectionID: resolved.ConnectionID,
	}, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
