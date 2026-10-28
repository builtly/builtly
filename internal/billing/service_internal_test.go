package billing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

func newMiniBilling(t *testing.T) (*Service, *db.DB, tenant.App, uuid.UUID, uuid.UUID) {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := make([]byte, 32)
	for i := range key {
		key[i] = 3
	}
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "mini"+uuid.Must(uuid.NewV7()).String()[:8], "M", tenant.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	now := db.Now()
	email := "m@ex.com"
	code := "MINICODE1"
	userID := uuid.Must(uuid.NewV7())
	u := auth.User{
		ID: userID, AppID: app.ID, Email: &email, DisplayName: "M", Status: auth.StatusActive,
		ReferralCode: &code, CreatedAt: now, UpdatedAt: now,
	}
	if err := auth.NewStore(database).Insert(context.Background(), nil, u); err != nil {
		t.Fatal(err)
	}
	prodID := uuid.Must(uuid.NewV7())
	if err := catalog.NewStore(database).Insert(context.Background(), nil, catalog.Product{
		ID: prodID, AppID: app.ID, SKU: "m", Name: "m", Kind: catalog.KindCreditPack,
		Credits: 50, PriceAmount: 500, Currency: "usd", Active: true,
		Metadata: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	oreg := outbox.NewRegistry()
	oreg.DefaultSubscriptions()
	pub := outbox.NewPublisher(outbox.NewStore(database), box, oreg)
	creditSvc := credit.NewService(database, credit.NewStore(database))
	iStore := integration.NewStore(database)
	env := integration.BuildEnvDefaults(&config.Config{})
	resolver := integration.NewResolver(iStore, box, env)
	integ := integration.NewService(database, iStore, box, env, "https://api.example.com", resolver)
	svc := NewService(database, NewStore(database), catalog.NewService(catalog.NewStore(database), resolver, nil),
		creditSvc, pub, resolver, integ, iStore, box, auth.NewStore(database), tSvc, nil, true)
	return svc, database, app, userID, prodID
}

func TestApplyRefundAndReversalEdges(t *testing.T) {
	svc, database, app, userID, prodID := newMiniBilling(t)
	ctx := context.Background()
	now := db.Now()
	pref := "cs_edge"
	pid := uuid.Must(uuid.NewV7())
	p := Purchase{
		ID: pid, AppID: app.ID, UserID: userID, ProductID: prodID,
		Provider: ProviderStripe, ConnectionID: "c", ProviderRef: &pref,
		Status: StatusCompleted, Credits: 50, RefundedCredits: 10, Environment: EnvProduction,
		CreatedAt: now, UpdatedAt: now, CompletedAt: db.NullTime{Time: now, Valid: true},
	}
	if err := svc.store.InsertPurchase(ctx, nil, p); err != nil {
		t.Fatal(err)
	}
	err := database.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := svc.applyRefund(ctx, tx, p, -5, "k-neg", "test"); err != nil {
			return err
		}
		if err := svc.applyRefund(ctx, tx, p, 5, "k-lt", "test"); err != nil { // target < refunded → noop
			return err
		}
		if err := svc.applyRefundReversal(ctx, tx, p, 0, "k-zero", "test"); err != nil {
			return err
		}
		if err := svc.applyRefundReversal(ctx, tx, p, 100, "k-big", "test"); err != nil {
			return err
		}
		return svc.fulfilCheckout(ctx, tx, app.ID, "missing-session", 0, "usd", "", "")
	})
	if err == nil {
		t.Fatal("want fulfil not found")
	}
	// already completed fulfil short-circuit
	err = database.WithTx(ctx, func(tx *sqlx.Tx) error {
		return svc.fulfilCheckout(ctx, tx, app.ID, pref, 500, "usd", "pi", pid.String())
	})
	if err != nil {
		t.Fatal(err)
	}
	// metadata mismatch
	err = database.WithTx(ctx, func(tx *sqlx.Tx) error {
		return svc.fulfilCheckout(ctx, tx, app.ID, pref, 500, "usd", "pi", uuid.Must(uuid.NewV7()).String())
	})
	if err == nil {
		t.Fatal("want mismatch")
	}
	_ = time.Now()
}

func TestLoadConnectionDefaultsAndMissing(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	key := make([]byte, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DefaultConnections: config.DefaultConnections{
			Stripe: &config.StripeDefault{SecretKey: "sk_test", WebhookSecrets: []string{"whsec_x"}},
			AppleIAP: &config.AppleIAPDefault{
				IssuerID: "iss", KeyID: "kid", BundleID: "com.app", PrivateKey: "-----BEGIN PRIVATE KEY-----\nMIGH\n-----END PRIVATE KEY-----",
			},
			GooglePlay: &config.GooglePlayDefault{Package: "com.app", ServiceAccount: `{"type":"service_account"}`},
		},
	}
	env := integration.BuildEnvDefaults(cfg)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	integ := integration.NewService(database, iStore, box, env, "https://api.example.com", resolver)
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	oreg := outbox.NewRegistry()
	oreg.DefaultSubscriptions()
	pub := outbox.NewPublisher(outbox.NewStore(database), box, oreg)
	svc := NewService(database, NewStore(database), catalog.NewService(catalog.NewStore(database), resolver, nil),
		credit.NewService(database, credit.NewStore(database)), pub, resolver, integ, iStore, box,
		auth.NewStore(database), tSvc, nil, true)

	ctx := context.Background()
	sec, settings, err := svc.loadStripeConnection(ctx, integration.DefaultConnectionID)
	if err != nil || sec.SecretKey != "sk_test" {
		t.Fatalf("stripe default: %+v %v", sec, err)
	}
	_ = settings
	if _, _, err := svc.loadStripeConnection(ctx, "missing"); err == nil {
		t.Fatal("stripe missing")
	}
	if _, err := svc.loadAppleIAPConnection(ctx, integration.DefaultConnectionID); err != nil {
		t.Fatalf("apple default: %v", err)
	}
	if _, err := svc.loadAppleIAPConnection(ctx, "missing"); err == nil {
		t.Fatal("apple missing")
	}
	p := NewVoidedPoller(svc)
	if _, err := p.loadGoogle(ctx, integration.DefaultConnectionID); err != nil {
		t.Fatalf("google default: %v", err)
	}
	if _, err := p.loadGoogle(ctx, "missing"); err == nil {
		t.Fatal("google missing")
	}
}
