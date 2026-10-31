package integration

// Provider identifiers.
const (
	ProviderStripe      = "stripe"
	ProviderResend      = "resend"
	ProviderR2          = "r2"
	ProviderGoogleOAuth = "google_oauth"
	ProviderGitHubOAuth = "github_oauth"
	ProviderAppleSignIn = "apple_signin"
	ProviderAppleIAP    = "apple_iap"
	ProviderGooglePlay  = "google_play"
	ProviderPostHog     = "posthog"
	ProviderPromoteKit  = "promotekit"
	ProviderAffonso     = "affonso"
	ProviderOpenAI      = "openai_compatible"
)

// DefaultConnectionID is the synthetic id for env-defined credentials.
const DefaultConnectionID = "default"

// ExclusiveProviders may bind to at most one app.
var ExclusiveProviders = map[string]bool{
	ProviderStripe:     true,
	ProviderAppleIAP:   true,
	ProviderGooglePlay: true,
}

// IsValidProvider reports whether p is a known provider.
func IsValidProvider(p string) bool {
	switch p {
	case ProviderStripe, ProviderResend, ProviderR2, ProviderGoogleOAuth, ProviderGitHubOAuth,
		ProviderAppleSignIn, ProviderAppleIAP, ProviderGooglePlay, ProviderPostHog,
		ProviderPromoteKit, ProviderAffonso, ProviderOpenAI:
		return true
	default:
		return false
	}
}

// IsExclusive reports whether the provider is exclusive.
func IsExclusive(p string) bool { return ExclusiveProviders[p] }
