package integration

import (
	"encoding/json"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/db"
)

// EnvDefaults holds synthetic read-only connections built from process config.
type EnvDefaults struct {
	byProvider map[string]Connection
	secrets    map[string][]byte // plaintext secret JSON per provider
}

// BuildEnvDefaults constructs default connections from config.
func BuildEnvDefaults(cfg *config.Config) *EnvDefaults {
	e := &EnvDefaults{
		byProvider: map[string]Connection{},
		secrets:    map[string][]byte{},
	}
	now := db.Now()
	add := func(provider, name string, settings any, sec any) {
		sb, _ := json.Marshal(settings)
		secb, _ := json.Marshal(sec)
		e.byProvider[provider] = Connection{
			ID:        DefaultConnectionID,
			Provider:  provider,
			Name:      name,
			Settings:  sb,
			CreatedAt: now,
			UpdatedAt: now,
			ReadOnly:  true,
		}
		e.secrets[provider] = secb
	}

	d := cfg.DefaultConnections
	if d.Stripe != nil {
		add(ProviderStripe, "env default stripe", map[string]any{
			"livemode": d.Stripe.LiveMode,
		}, StripeSecrets{
			SecretKey:      d.Stripe.SecretKey,
			WebhookSecrets: d.Stripe.WebhookSecrets,
		})
	}
	if d.Resend != nil {
		add(ProviderResend, "env default resend", map[string]any{}, APIKeySecrets{APIKey: d.Resend.APIKey})
	}
	if d.R2 != nil {
		add(ProviderR2, "env default r2", map[string]any{
			"account_id": d.R2.AccountID,
			"bucket":     d.R2.Bucket,
		}, R2Secrets{
			AccessKeyID:     d.R2.AccessKeyID,
			SecretAccessKey: d.R2.SecretAccessKey,
		})
	}
	if d.GoogleOAuth != nil {
		add(ProviderGoogleOAuth, "env default google_oauth", map[string]any{
			"client_id":         d.GoogleOAuth.ClientID,
			"native_client_ids": d.GoogleOAuth.NativeClientIDs,
		}, OAuthSecrets{ClientSecret: d.GoogleOAuth.ClientSecret})
	}
	if d.GitHubOAuth != nil {
		add(ProviderGitHubOAuth, "env default github_oauth", map[string]any{
			"client_id": d.GitHubOAuth.ClientID,
		}, OAuthSecrets{ClientSecret: d.GitHubOAuth.ClientSecret})
	}
	if d.AppleSignIn != nil {
		add(ProviderAppleSignIn, "env default apple_signin", map[string]any{
			"team_id":     d.AppleSignIn.TeamID,
			"key_id":      d.AppleSignIn.KeyID,
			"services_id": d.AppleSignIn.ServicesID,
			"bundle_ids":  d.AppleSignIn.BundleIDs,
		}, PEMSecrets{PrivateKeyPEM: d.AppleSignIn.PrivateKey})
	}
	if d.AppleIAP != nil {
		add(ProviderAppleIAP, "env default apple_iap", map[string]any{
			"issuer_id": d.AppleIAP.IssuerID,
			"key_id":    d.AppleIAP.KeyID,
			"bundle_id": d.AppleIAP.BundleID,
		}, PEMSecrets{PrivateKeyPEM: d.AppleIAP.PrivateKey})
	}
	if d.GooglePlay != nil {
		add(ProviderGooglePlay, "env default google_play", map[string]any{
			"package_name": d.GooglePlay.Package,
		}, GooglePlaySecrets{ServiceAccountJSON: d.GooglePlay.ServiceAccount})
	}
	if d.PostHog != nil {
		add(ProviderPostHog, "env default posthog", map[string]any{
			"host": d.PostHog.Host,
		}, PostHogSecrets{ProjectAPIKey: d.PostHog.APIKey})
	}
	if d.PromoteKit != nil {
		add(ProviderPromoteKit, "env default promotekit", map[string]any{}, APIKeySecrets{APIKey: d.PromoteKit.APIKey})
	}
	if d.Affonso != nil {
		add(ProviderAffonso, "env default affonso", map[string]any{}, APIKeySecrets{APIKey: d.Affonso.APIKey})
	}
	return e
}

// Get returns the env default connection and plaintext secrets for a provider.
func (e *EnvDefaults) Get(provider string) (Connection, []byte, bool) {
	c, ok := e.byProvider[provider]
	if !ok {
		return Connection{}, nil, false
	}
	return c, e.secrets[provider], true
}

// List returns all env default connections.
func (e *EnvDefaults) List() []Connection {
	out := make([]Connection, 0, len(e.byProvider))
	for _, c := range e.byProvider {
		out = append(out, c)
	}
	return out
}

// Has reports whether a default exists for provider.
func (e *EnvDefaults) Has(provider string) bool {
	_, ok := e.byProvider[provider]
	return ok
}
