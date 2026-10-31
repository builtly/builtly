// Package provider implements OAuth/OIDC adapters for Google, GitHub and Apple.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/jwks"
)

// Names used in settings.auth.providers and user_identities.provider.
const (
	Google = "google"
	GitHub = "github"
	Apple  = "apple"
)

// Identity is a verified provider identity.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
	RefreshToken  string // Apple only
	Audience      string // Verified aud claim
}

// WebProvider is the browser redirect OAuth adapter.
type WebProvider interface {
	AuthCodeURL(cfg Config, state, nonce, pkceVerifier, redirectURI string) string
	Exchange(ctx context.Context, cfg Config, code, pkceVerifier, nonce, redirectURI string) (Identity, error)
}

// NativeProvider verifies native SDK ID tokens.
type NativeProvider interface {
	VerifyIDToken(ctx context.Context, cfg Config, idToken, nonce string) (Identity, error)
}

// Config is resolved connection settings + secrets for one OAuth provider.
type Config struct {
	ClientID        string
	ClientSecret    string
	NativeClientIDs []string
	TeamID          string
	KeyID           string
	ServicesID      string
	BundleIDs       []string
	PrivateKeyPEM   string
}

// Registry holds concrete adapters.
type Registry struct {
	Google WebProvider
	GitHub WebProvider
	Apple  WebProvider
	Native struct {
		Google NativeProvider
		Apple  NativeProvider
	}
	AppleRevoke func(ctx context.Context, cfg Config, refreshToken string) error
}

// NewRegistry constructs adapters with shared HTTP/JWKS.
func NewRegistry(hc *http.Client, cache *jwks.Cache) *Registry {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	if cache == nil {
		cache = jwks.New(hc)
	}
	g := &googleProvider{http: hc, jwks: cache}
	a := &appleProvider{http: hc, jwks: cache}
	return &Registry{
		Google: g,
		GitHub: &githubProvider{http: hc},
		Apple:  a,
		Native: struct {
			Google NativeProvider
			Apple  NativeProvider
		}{Google: g, Apple: a},
		AppleRevoke: a.Revoke,
	}
}

// Web returns the web adapter for a short provider name.
func (r *Registry) Web(name string) (WebProvider, bool) {
	switch name {
	case Google:
		return r.Google, true
	case GitHub:
		return r.GitHub, true
	case Apple:
		return r.Apple, true
	default:
		return nil, false
	}
}

// IntegrationProvider maps auth provider → connections.provider.
func IntegrationProvider(name string) string {
	switch name {
	case Google:
		return integration.ProviderGoogleOAuth
	case GitHub:
		return integration.ProviderGitHubOAuth
	case Apple:
		return integration.ProviderAppleSignIn
	default:
		return ""
	}
}

// ParseConfig builds Config from a resolved integration connection.
func ParseConfig(provider string, settings, secretsJSON []byte) (Config, error) {
	var cfg Config
	var settingsMap map[string]any
	_ = json.Unmarshal(settings, &settingsMap)
	switch provider {
	case integration.ProviderGoogleOAuth:
		// Web client ID (+ secret below). iOS/Android are separate Google Cloud
		// OAuth clients; also accept legacy native_client_ids[].
		cfg.ClientID = str(settingsMap["client_id"])
		natives := append([]string{
			str(settingsMap["ios_client_id"]),
			str(settingsMap["android_client_id"]),
		}, strSlice(settingsMap["native_client_ids"])...)
		cfg.NativeClientIDs = uniqueStrings(natives...)
		var sec integration.OAuthSecrets
		if err := json.Unmarshal(secretsJSON, &sec); err != nil {
			return Config{}, err
		}
		cfg.ClientSecret = sec.ClientSecret
	case integration.ProviderGitHubOAuth:
		cfg.ClientID = str(settingsMap["client_id"])
		var sec integration.OAuthSecrets
		if err := json.Unmarshal(secretsJSON, &sec); err != nil {
			return Config{}, err
		}
		cfg.ClientSecret = sec.ClientSecret
	case integration.ProviderAppleSignIn:
		cfg.TeamID = str(settingsMap["team_id"])
		cfg.KeyID = str(settingsMap["key_id"])
		cfg.ServicesID = str(settingsMap["services_id"])
		cfg.BundleIDs = strSlice(settingsMap["bundle_ids"])
		cfg.ClientID = cfg.ServicesID
		var sec integration.PEMSecrets
		if err := json.Unmarshal(secretsJSON, &sec); err != nil {
			return Config{}, err
		}
		cfg.PrivateKeyPEM = sec.PrivateKeyPEM
	default:
		return Config{}, fmt.Errorf("provider: unsupported %s", provider)
	}
	return cfg, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func uniqueStrings(vals ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
