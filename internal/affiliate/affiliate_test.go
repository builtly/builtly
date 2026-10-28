package affiliate_test

import (
	"testing"

	"github.com/builtly/builtly/internal/affiliate"
	"github.com/builtly/builtly/internal/tenant"
)

func TestCheckoutFields(t *testing.T) {
	reg := affiliate.NewRegistry(nil)
	app := tenant.App{Settings: tenant.Settings{Affiliate: tenant.AffiliateSettings{Provider: "promotekit"}}}
	fields := affiliate.CheckoutFieldsForApp(reg, app, "ref123")
	if fields["metadata[promotekit_referral]"] != "ref123" {
		t.Fatalf("%v", fields)
	}
	app.Settings.Affiliate.Provider = "affonso"
	fields = affiliate.CheckoutFieldsForApp(reg, app, "ref456")
	if fields["metadata[affonso_referral]"] != "ref456" {
		t.Fatalf("%v", fields)
	}
	app.Settings.Affiliate.Provider = "none"
	if affiliate.CheckoutFieldsForApp(reg, app, "x") != nil {
		t.Fatal("expected nil")
	}
}
