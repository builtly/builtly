package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

func testKeys() []config.KeyMaterial {
	return []config.KeyMaterial{{
		ID:  "1",
		Key: []byte("0123456789abcdef0123456789abcdef"),
	}}
}

func setup(t *testing.T) (context.Context, *tenant.Service, *integration.Service, *integration.Resolver) {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	box, err := secrets.NewBox(testKeys())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		PublicURL: "https://api.example.com",
		DefaultConnections: config.DefaultConnections{
			Resend: &config.ResendDefault{APIKey: "re_test_secret_key_value"},
			Stripe: &config.StripeDefault{
				SecretKey:      "sk_test_aaaaaaaaaaaaaaaaaaaaaaaa",
				WebhookSecrets: []string{"whsec_bbbbbbbbbbbbbbbbbbbb"},
				LiveMode:       false,
			},
		},
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	return context.Background(), tSvc, iSvc, resolver
}

func TestResolverPrecedence(t *testing.T) {
	ctx, tSvc, iSvc, resolver := setup(t)

	app, err := tSvc.Create(ctx, "acme", "Acme", tenant.Settings{
		AllowedOrigins: []string{"https://acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app2, err := tSvc.Create(ctx, "beta", "Beta", tenant.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("shared implicit env default", func(t *testing.T) {
		r, err := resolver.For(ctx, app.ID, integration.ProviderResend)
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != "default" || r.ConnectionID != integration.DefaultConnectionID {
			t.Fatalf("got %+v", r)
		}
		var sec integration.APIKeySecrets
		_ = json.Unmarshal(r.Secrets, &sec)
		if sec.APIKey != "re_test_secret_key_value" {
			t.Fatalf("secret %q", sec.APIKey)
		}
	})

	t.Run("exclusive no implicit fallback", func(t *testing.T) {
		_, err := resolver.For(ctx, app.ID, integration.ProviderStripe)
		if err != integration.ErrNotConfigured {
			t.Fatalf("want not configured, got %v", err)
		}
	})

	t.Run("binding to env default", func(t *testing.T) {
		err := iSvc.ReplaceBindingsForApp(ctx, app.ID, map[string]integration.BindingInput{
			integration.ProviderStripe: {ConnectionID: "default"},
		})
		if err != nil {
			t.Fatal(err)
		}
		r, err := resolver.For(ctx, app.ID, integration.ProviderStripe)
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != "binding" || r.ConnectionID != "default" {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("exclusive default only one app", func(t *testing.T) {
		err := iSvc.ReplaceBindingsForApp(ctx, app2.ID, map[string]integration.BindingInput{
			integration.ProviderStripe: {ConnectionID: "default"},
		})
		if err == nil {
			t.Fatal("expected connection_already_bound")
		}
	})

	t.Run("binding to stored connection", func(t *testing.T) {
		pc, err := iSvc.CreateConnection(ctx, integration.ProviderResend, "mail",
			json.RawMessage(`{}`),
			mustJSON(t, integration.APIKeySecrets{APIKey: "re_app_specific_key_zzzz"}))
		if err != nil {
			t.Fatal(err)
		}
		// masked must not contain full secret
		if string(pc.Secrets) != "" && contains(string(pc.Secrets), "re_app_specific_key_zzzz") {
			t.Fatalf("secret leaked in public view: %s", pc.Secrets)
		}
		err = iSvc.ReplaceBindingsForApp(ctx, app.ID, map[string]integration.BindingInput{
			integration.ProviderResend: {ConnectionID: pc.ID},
			integration.ProviderStripe: {ConnectionID: "default"},
		})
		if err != nil {
			t.Fatal(err)
		}
		resolver.Invalidate()
		r, err := resolver.For(ctx, app.ID, integration.ProviderResend)
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != "binding" || r.ConnectionID != pc.ID {
			t.Fatalf("got %+v", r)
		}
		var sec integration.APIKeySecrets
		_ = json.Unmarshal(r.Secrets, &sec)
		if sec.APIKey != "re_app_specific_key_zzzz" {
			t.Fatalf("got %q", sec.APIKey)
		}
	})

	t.Run("unconfigured shared without env", func(t *testing.T) {
		_, err := resolver.For(ctx, app.ID, integration.ProviderPostHog)
		if err == nil {
			t.Fatal("expected not configured")
		}
	})

}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func contains(s, sub string) bool {
	return len(sub) > 0 && (s == sub || len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
