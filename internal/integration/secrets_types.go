package integration

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/builtly/builtly/internal/platform/secrets"
)

// Provider-specific secret payloads (plaintext JSON sealed at rest).

type StripeSecrets struct {
	SecretKey      string   `json:"secret_key"`
	WebhookSecrets []string `json:"webhook_secrets"`
}

type APIKeySecrets struct {
	APIKey string `json:"api_key"`
}

type R2Secrets struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

type OAuthSecrets struct {
	ClientSecret string `json:"client_secret"`
}

type PEMSecrets struct {
	PrivateKeyPEM string `json:"private_key_pem"`
}

type GooglePlaySecrets struct {
	ServiceAccountJSON string `json:"service_account_json"`
}

type PostHogSecrets struct {
	ProjectAPIKey string `json:"project_api_key"`
}

type OpenAISecrets struct {
	APIKey string `json:"api_key"`
}

// MaskSecretsJSON returns a JSON object with secret values masked for API responses.
func MaskSecretsJSON(provider string, plain []byte) (json.RawMessage, error) {
	if len(plain) == 0 {
		return json.RawMessage(`{}`), nil
	}
	switch provider {
	case ProviderStripe:
		var s StripeSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"secret_key":      secrets.MaskSecret(s.SecretKey),
			"webhook_secrets": secrets.MaskSecretList(s.WebhookSecrets),
		})
	case ProviderResend, ProviderPromoteKit, ProviderAffonso, ProviderOpenAI:
		var s APIKeySecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"api_key": secrets.MaskSecret(s.APIKey)})
	case ProviderR2:
		var s R2Secrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"access_key_id":     secrets.MaskSecret(s.AccessKeyID),
			"secret_access_key": secrets.MaskSecret(s.SecretAccessKey),
		})
	case ProviderGoogleOAuth, ProviderGitHubOAuth:
		var s OAuthSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"client_secret": secrets.MaskSecret(s.ClientSecret)})
	case ProviderAppleSignIn, ProviderAppleIAP:
		var s PEMSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"private_key_pem": secrets.MaskSecret(s.PrivateKeyPEM)})
	case ProviderGooglePlay:
		var s GooglePlaySecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"service_account_json": secrets.MaskSecret(s.ServiceAccountJSON)})
	case ProviderPostHog:
		var s PostHogSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"project_api_key": secrets.MaskSecret(s.ProjectAPIKey)})
	default:
		return nil, fmt.Errorf("unknown provider %s", provider)
	}
}

// NormalizeSecretsJSON trims whitespace on Stripe secrets before sealing.
func NormalizeSecretsJSON(provider string, plain []byte) (json.RawMessage, error) {
	if provider != ProviderStripe || len(plain) == 0 {
		return json.RawMessage(plain), nil
	}
	var s StripeSecrets
	if err := json.Unmarshal(plain, &s); err != nil {
		return nil, err
	}
	s.SecretKey = strings.TrimSpace(s.SecretKey)
	wh := make([]string, 0, len(s.WebhookSecrets))
	for _, w := range s.WebhookSecrets {
		if t := strings.TrimSpace(w); t != "" {
			wh = append(wh, t)
		}
	}
	s.WebhookSecrets = wh
	return json.Marshal(s)
}

// ValidateSecretsJSON checks required secret fields for a provider.
func ValidateSecretsJSON(provider string, plain []byte) error {
	if len(plain) == 0 {
		return fmt.Errorf("secrets required")
	}
	switch provider {
	case ProviderStripe:
		var s StripeSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		validWH := 0
		for _, w := range s.WebhookSecrets {
			if strings.TrimSpace(w) != "" {
				validWH++
			}
		}
		if strings.TrimSpace(s.SecretKey) == "" || validWH == 0 {
			return fmt.Errorf("stripe requires secret_key and webhook_secrets")
		}
	case ProviderResend, ProviderPromoteKit, ProviderAffonso, ProviderOpenAI:
		var s APIKeySecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.APIKey == "" {
			return fmt.Errorf("api_key required")
		}
	case ProviderR2:
		var s R2Secrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.AccessKeyID == "" || s.SecretAccessKey == "" {
			return fmt.Errorf("r2 requires access_key_id and secret_access_key")
		}
	case ProviderGoogleOAuth, ProviderGitHubOAuth:
		var s OAuthSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.ClientSecret == "" {
			return fmt.Errorf("client_secret required")
		}
	case ProviderAppleSignIn, ProviderAppleIAP:
		var s PEMSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.PrivateKeyPEM == "" {
			return fmt.Errorf("private_key_pem required")
		}
	case ProviderGooglePlay:
		var s GooglePlaySecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.ServiceAccountJSON == "" {
			return fmt.Errorf("service_account_json required")
		}
	case ProviderPostHog:
		var s PostHogSecrets
		if err := json.Unmarshal(plain, &s); err != nil {
			return err
		}
		if s.ProjectAPIKey == "" {
			return fmt.Errorf("project_api_key required")
		}
	default:
		return fmt.Errorf("unknown provider")
	}
	return nil
}

// MergePatchJSON merges a partial JSON object onto an existing JSON object.
// Omitted keys keep their existing value; keys with a JSON null value are removed.
func MergePatchJSON(existing, patch []byte) (json.RawMessage, error) {
	base := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &base); err != nil {
			return nil, err
		}
	}
	if base == nil {
		base = map[string]any{}
	}
	if len(patch) == 0 || string(patch) == "null" {
		b, err := json.Marshal(base)
		return b, err
	}
	var over map[string]any
	if err := json.Unmarshal(patch, &over); err != nil {
		return nil, err
	}
	for k, v := range over {
		if v == nil {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	b, err := json.Marshal(base)
	return b, err
}

// RedactDiff replaces secret-looking values in a diff map with "[redacted]".
func RedactDiff(diff map[string]any) map[string]any {
	out := make(map[string]any, len(diff))
	for k, v := range diff {
		switch k {
		case "secrets", "secret_key", "webhook_secrets", "api_key", "client_secret",
			"private_key_pem", "service_account_json", "project_api_key",
			"access_key_id", "secret_access_key", "key", "token", "password":
			out[k] = "[redacted]"
		default:
			if m, ok := v.(map[string]any); ok {
				out[k] = RedactDiff(m)
			} else {
				out[k] = v
			}
		}
	}
	return out
}
