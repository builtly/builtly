package tenant

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Settings is the non-secret per-app configuration stored in apps.settings.
type Settings struct {
	AllowedOrigins     []string          `json:"allowed_origins"`
	AuthRedirectURLs   []string          `json:"auth_redirect_urls"`
	Auth               AuthSettings      `json:"auth"`
	SignupBonusCredits int64             `json:"signup_bonus_credits"`
	AllowIAPSandbox    bool              `json:"allow_iap_sandbox"`
	Email              EmailSettings     `json:"email"`
	Storage            StorageSettings   `json:"storage"`
	Analytics          AnalyticsSettings `json:"analytics"`
	Affiliate          AffiliateSettings `json:"affiliate"`
	Referral           ReferralSettings  `json:"referral"`
	AI                 AISettings        `json:"ai"`
}

// AISettings configures the built-in AI gateway.
type AISettings struct {
	Enabled              bool              `json:"enabled"`
	AllowDirectChat      bool              `json:"allow_direct_chat"`
	MaxConcurrentPerUser int               `json:"max_concurrent_per_user"`
	MaxContextMessages   int               `json:"max_context_messages"`
	DefaultMaxTokens     int               `json:"default_max_tokens"`
	Pricing              AIPricingSettings `json:"pricing"`
}

// AIPricingSettings controls AI credit deduction rates.
type AIPricingSettings struct {
	Mode                     string                    `json:"mode"` // per_request | per_token
	DefaultCreditsPerRequest int64                     `json:"default_credits_per_request"`
	Models                   map[string]AIModelPricing `json:"models"`
}

// AIModelPricing sets credit cost for a specific model.
type AIModelPricing struct {
	CreditsPerRequest  int64 `json:"credits_per_request"`
	InputCreditsPer1K  int64 `json:"input_credits_per_1k"`
	OutputCreditsPer1K int64 `json:"output_credits_per_1k"`
	MinCredits         int64 `json:"min_credits"`
}

// AuthSettings controls sign-in methods.
type AuthSettings struct {
	PasswordEnabled          bool     `json:"password_enabled"`
	Providers                []string `json:"providers"`
	RequireEmailVerification bool     `json:"require_email_verification"`
}

// EmailSettings is the transactional sender identity.
type EmailSettings struct {
	FromName      string `json:"from_name"`
	FromAddress   string `json:"from_address"`
	ReplyTo       string `json:"reply_to"`
	ActionBaseURL string `json:"action_base_url"`
}

// StorageSettings controls R2 upload limits.
type StorageSettings struct {
	MaxFileBytes        int64    `json:"max_file_bytes"`
	UserQuotaBytes      int64    `json:"user_quota_bytes"`
	AllowedContentTypes []string `json:"allowed_content_types"`
}

// AnalyticsSettings toggles optional event capture.
type AnalyticsSettings struct {
	CaptureConsumeEvents bool `json:"capture_consume_events"`
}

// AffiliateSettings selects an external affiliate provider.
type AffiliateSettings struct {
	Provider string `json:"provider"` // none | promotekit | affonso
}

// ReferralSettings configures the internal credit referral program.
type ReferralSettings struct {
	Enabled             bool   `json:"enabled"`
	Trigger             string `json:"trigger"`     // first_purchase | every_purchase
	RewardType          string `json:"reward_type"` // percent | fixed
	RewardValue         int64  `json:"reward_value"`
	RefereeBonusCredits int64  `json:"referee_bonus_credits"`
	MaxRewardedReferees int64  `json:"max_rewarded_referees"`
	ApplyWindowDays     int    `json:"apply_window_days"`
}

// DefaultSettings returns the baseline settings merged on create.
func DefaultSettings() Settings {
	return Settings{
		AllowedOrigins:   []string{},
		AuthRedirectURLs: []string{},
		Auth: AuthSettings{
			PasswordEnabled: true,
			Providers:       []string{},
		},
		SignupBonusCredits: 0,
		Email:              EmailSettings{},
		Storage: StorageSettings{
			MaxFileBytes:        10 << 20,
			UserQuotaBytes:      100 << 20,
			AllowedContentTypes: []string{"image/png", "image/jpeg", "image/webp", "application/pdf"},
		},
		Analytics: AnalyticsSettings{},
		Affiliate: AffiliateSettings{Provider: "none"},
		Referral: ReferralSettings{
			Enabled:         false,
			Trigger:         "first_purchase",
			RewardType:      "percent",
			RewardValue:     10,
			ApplyWindowDays: 7,
		},
		AI: AISettings{
			Enabled:              true,
			AllowDirectChat:      true,
			MaxConcurrentPerUser: 2,
			MaxContextMessages:   40,
			DefaultMaxTokens:     2048,
			Pricing: AIPricingSettings{
				Mode:                     "per_request",
				DefaultCreditsPerRequest: 1,
				Models:                   map[string]AIModelPricing{},
			},
		},
	}
}

// MergeSettings overlays non-empty fields from overlay onto base (for Go struct callers).
func MergeSettings(base Settings, overlay Settings) (Settings, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return Settings{}, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(baseJSON, &merged); err != nil {
		return Settings{}, err
	}
	overJSON, err := json.Marshal(overlay)
	if err != nil {
		return Settings{}, err
	}
	var over map[string]json.RawMessage
	if err := json.Unmarshal(overJSON, &over); err != nil {
		return Settings{}, err
	}
	// Only apply top-level keys that differ from an empty Settings marshal of zeros for "presence".
	emptyJSON, _ := json.Marshal(Settings{})
	var empty map[string]json.RawMessage
	_ = json.Unmarshal(emptyJSON, &empty)
	for k, v := range over {
		if string(v) == string(empty[k]) {
			continue
		}
		// For nested objects, deep-merge.
		if k == "auth" || k == "email" || k == "storage" || k == "analytics" || k == "affiliate" || k == "referral" || k == "ai" {
			var baseObj, overObj map[string]json.RawMessage
			_ = json.Unmarshal(merged[k], &baseObj)
			_ = json.Unmarshal(v, &overObj)
			var emptyObj map[string]json.RawMessage
			_ = json.Unmarshal(empty[k], &emptyObj)
			for ok, ov := range overObj {
				if string(ov) == string(emptyObj[ok]) {
					continue
				}
				baseObj[ok] = ov
			}
			merged[k], _ = json.Marshal(baseObj)
			continue
		}
		merged[k] = v
	}
	outJSON, err := json.Marshal(merged)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	if err := json.Unmarshal(outJSON, &out); err != nil {
		return Settings{}, err
	}
	return normalizeSettings(out), nil
}

// MergeSettingsJSON overlays raw JSON keys onto base, preserving explicit false/0/empty values.
func MergeSettingsJSON(base Settings, raw []byte) (Settings, error) {
	out := base
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return normalizeSettings(out), nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Settings{}, fmt.Errorf("tenant: settings: %w", err)
	}
	return normalizeSettings(out), nil
}

func normalizeSettings(s Settings) Settings {
	if s.AllowedOrigins == nil {
		s.AllowedOrigins = []string{}
	}
	if s.AuthRedirectURLs == nil {
		s.AuthRedirectURLs = []string{}
	}
	if s.Auth.Providers == nil {
		s.Auth.Providers = []string{}
	}
	if s.Storage.AllowedContentTypes == nil {
		s.Storage.AllowedContentTypes = []string{}
	}
	if s.Affiliate.Provider == "" {
		s.Affiliate.Provider = "none"
	}
	return s
}

// ParseSettings unmarshals JSON with defaults applied for missing fields.
func ParseSettings(raw []byte) (Settings, error) {
	s, err := MergeSettingsJSON(DefaultSettings(), raw)
	if err != nil {
		return Settings{}, err
	}
	if err := s.Validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Validate checks settings invariants.
func (s Settings) Validate() error {
	for _, o := range s.AllowedOrigins {
		if o == "" {
			return fmt.Errorf("tenant: allowed_origins entry empty")
		}
		if strings.Contains(o, "*") {
			return fmt.Errorf("tenant: allowed_origins must not contain wildcards")
		}
		u, err := url.Parse(o)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("tenant: invalid allowed_origin %q", o)
		}
	}
	for _, r := range s.AuthRedirectURLs {
		if r == "" {
			return fmt.Errorf("tenant: auth_redirect_urls entry empty")
		}
	}
	for _, p := range s.Auth.Providers {
		switch p {
		case "google", "github", "apple":
		default:
			return fmt.Errorf("tenant: unknown auth provider %q", p)
		}
	}
	if s.SignupBonusCredits < 0 {
		return fmt.Errorf("tenant: signup_bonus_credits must be >= 0")
	}
	switch s.Affiliate.Provider {
	case "", "none", "promotekit", "affonso":
	default:
		return fmt.Errorf("tenant: unknown affiliate provider %q", s.Affiliate.Provider)
	}
	switch s.Referral.Trigger {
	case "first_purchase", "every_purchase":
	default:
		return fmt.Errorf("tenant: referral.trigger must be first_purchase or every_purchase")
	}
	switch s.Referral.RewardType {
	case "percent", "fixed":
	default:
		return fmt.Errorf("tenant: referral.reward_type must be percent or fixed")
	}
	if s.Referral.RewardType == "percent" && (s.Referral.RewardValue < 1 || s.Referral.RewardValue > 100) {
		return fmt.Errorf("tenant: referral.reward_value percent must be 1–100")
	}
	if s.Referral.RewardType == "fixed" && s.Referral.RewardValue <= 0 && s.Referral.Enabled {
		return fmt.Errorf("tenant: referral.reward_value fixed must be > 0 when enabled")
	}
	if s.Referral.ApplyWindowDays < 0 {
		return fmt.Errorf("tenant: referral.apply_window_days must be >= 0")
	}
	if s.Storage.MaxFileBytes <= 0 || s.Storage.UserQuotaBytes <= 0 {
		return fmt.Errorf("tenant: storage limits must be > 0")
	}
	return nil
}

// AllowsOrigin reports whether origin is in the allow list (exact match).
func (s Settings) AllowsOrigin(origin string) bool {
	for _, o := range s.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// Marshal returns JSON for persistence.
func (s Settings) Marshal() (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return b, nil
}
