package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/tenant"
)

func TestReferralPurchasesAdapter(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "rpadapter", "RP", tenant.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	now := db.Now()
	prodID := uuid.Must(uuid.NewV7())
	_ = catalog.NewStore(database).Insert(context.Background(), nil, catalog.Product{
		ID: prodID, AppID: app.ID, SKU: "p", Name: "p", Kind: catalog.KindCreditPack,
		Credits: 10, PriceAmount: 100, Currency: "usd", Active: true,
		Metadata: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	})
	userID := uuid.Must(uuid.NewV7())
	email := "u@ex.com"
	code := "CODE1234"
	// minimal user row for FK if purchases require users — check schema
	_, err = database.ExecContext(context.Background(), database.Rebind(`
		INSERT INTO users (id, app_id, email, display_name, avatar_url, status, attribution, referral_code, created_at, updated_at)
		VALUES (?, ?, ?, '', '', 'active', '{}', ?, ?, ?)`),
		userID.String(), app.ID.String(), email, code, time.Time(now).UTC().Format(time.RFC3339Nano), time.Time(now).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	store := billing.NewStore(database)
	pref := "tok"
	pid := uuid.Must(uuid.NewV7())
	if err := store.InsertPurchase(context.Background(), nil, billing.Purchase{
		ID: pid, AppID: app.ID, UserID: userID, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c", ProviderRef: &pref,
		Status: billing.StatusCompleted, Credits: 10, Environment: billing.EnvProduction,
		CreatedAt: now, UpdatedAt: now, CompletedAt: db.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	adapter := billing.ReferralPurchases{Store: store}
	info, err := adapter.GetPurchase(context.Background(), nil, app.ID, pid)
	if err != nil || info.Credits != 10 {
		t.Fatalf("%+v %v", info, err)
	}
	ok, err := adapter.HasCompletedPurchase(context.Background(), nil, app.ID, userID)
	if err != nil || !ok {
		t.Fatalf("has=%v err=%v", ok, err)
	}
}
