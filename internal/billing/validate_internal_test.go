package billing

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/billing/appstore"
	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/tenant"
)

func TestValidateAppleTxVariants(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	rev := int64(1)
	cases := []struct {
		name string
		tx   appstore.Transaction
		want string
	}{
		{"ok", appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Production", AppAccountToken: uid.String()}, ""},
		{"wrong_app", appstore.Transaction{BundleID: "x", Type: "Consumable", Environment: "Production"}, "wrong_app"},
		{"not_consumable", appstore.Transaction{BundleID: "b", Type: "Sub", Environment: "Production"}, "not_consumable"},
		{"revoked", appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Production", RevocationDate: &rev}, "revoked"},
		{"sandbox", appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Sandbox"}, "sandbox_not_allowed"},
		{"acct", appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Production", AppAccountToken: uuid.Must(uuid.NewV7()).String()}, "account_mismatch"},
	}
	for _, tc := range cases {
		if got := validateAppleTx(tc.tx, "b", false, false, uid); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	if got := validateAppleTx(appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Sandbox"}, "b", true, false, uid); got != "" {
		t.Fatalf("sandbox allowed: %q", got)
	}
	if got := validateAppleTx(appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Sandbox", AppAccountToken: uid.String()}, "b", true, true, uid); got != "" {
		t.Fatalf("allowlist sandbox: %q", got)
	}
	if got := validateAppleTx(appstore.Transaction{BundleID: "b", Type: "Consumable", Environment: "Sandbox"}, "b", true, true, uid); got != "account_token_required" {
		t.Fatalf("allowlist missing token: %q", got)
	}
}

func TestValidateGooglePurchaseVariants(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	pt := 0
	if got := validateGooglePurchase(playstore.ProductPurchase{PurchaseState: 1}, true, false, uid); got != "not_found" {
		t.Fatalf("state: %q", got)
	}
	if got := validateGooglePurchase(playstore.ProductPurchase{PurchaseState: 0, PurchaseType: &pt}, false, false, uid); got != "sandbox_not_allowed" {
		t.Fatalf("sandbox: %q", got)
	}
	if got := validateGooglePurchase(playstore.ProductPurchase{
		PurchaseState: 0, ObfuscatedExternalAccountID: uuid.Must(uuid.NewV7()).String(),
	}, true, false, uid); got != "account_mismatch" {
		t.Fatalf("acct: %q", got)
	}
	if got := validateGooglePurchase(playstore.ProductPurchase{PurchaseState: 0}, true, false, uid); got != "" {
		t.Fatalf("ok: %q", got)
	}
	if got := validateGooglePurchase(playstore.ProductPurchase{
		PurchaseState: 0, PurchaseType: &pt, ObfuscatedExternalAccountID: uid.String(),
	}, true, true, uid); got != "" {
		t.Fatalf("allowlist sandbox: %q", got)
	}
	if got := validateGooglePurchase(playstore.ProductPurchase{PurchaseState: 0, PurchaseType: &pt}, true, true, uid); got != "account_token_required" {
		t.Fatalf("allowlist missing token: %q", got)
	}
}

func TestRefundCreditsHelper(t *testing.T) {
	amt := int64(1000)
	p := Purchase{Credits: 100, Amount: &amt}
	if got := refundCredits(p, 1000, 1000); got != 100 {
		t.Fatalf("full %d", got)
	}
	if got := refundCredits(p, 500, 1000); got != 50 {
		t.Fatalf("half %d", got)
	}
	p2 := Purchase{Credits: 10}
	if got := refundCredits(p2, 1, 0); got != 10 {
		t.Fatalf("zero charge %d", got)
	}
}

func TestMustParseUUID(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	if mustParseUUID(id.String()) != id {
		t.Fatal("roundtrip")
	}
	if mustParseUUID("bad") != uuid.Nil {
		t.Fatal("bad")
	}
}

func TestIAPGrantKeyHelpers(t *testing.T) {
	if key := iapGrantKey(ProviderApple, "t"); key != "iap:apple:t" {
		t.Fatalf("apple %q", key)
	}
	if key := iapGrantKey(ProviderGoogle, "tok"); key != "iap:google:tok" {
		t.Fatalf("google %q", key)
	}
	if key := iapGrantKey("other", "r"); key != "iap:other:r" {
		t.Fatalf("other %q", key)
	}
	if googleProviderRef("tok") == "" {
		t.Fatal("hash")
	}
}

func TestAttributionAffiliate(t *testing.T) {
	if attributionAffiliate(nil) != "" {
		t.Fatal("nil")
	}
	if attributionAffiliate([]byte(`{`)) != "" {
		t.Fatal("bad json")
	}
	if attributionAffiliate([]byte(`{"affiliate_ref":"a1"}`)) != "a1" {
		t.Fatal("ok")
	}
}

func TestJobStoreRoundTrip(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	js := NewJobStore(database)
	if _, ok, err := js.Get(context.Background(), "missing"); err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	if err := js.Set(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := js.Get(context.Background(), "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
}

func TestValidateCheckoutURLs(t *testing.T) {
	app := tenant.App{Settings: tenant.Settings{AllowedOrigins: []string{"https://ok.com", "http://localhost:3000"}}}
	if err := validateCheckoutURLs(app, "https://ok.com/t", "https://ok.com/c", false); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckoutURLs(app, "http://localhost:3000/t", "http://localhost:3000/c", true); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckoutURLs(app, "http://localhost:3000/t", "http://localhost:3000/c", false); err == nil {
		t.Fatal("http blocked")
	}
	if err := validateCheckoutURLs(app, "ftp://ok.com/t", "https://ok.com/c", true); err == nil {
		t.Fatal("ftp")
	}
	if err := validateCheckoutURLs(app, "https://evil.com/t", "https://ok.com/c", true); err == nil {
		t.Fatal("origin")
	}
	if err := validateCheckoutURLs(app, "://bad", "https://ok.com/c", true); err == nil {
		t.Fatal("parse")
	}
}

func TestStrHelper(t *testing.T) {
	if str("x") != "x" || str(1) != "" {
		t.Fatal("str")
	}
}
