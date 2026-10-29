package integration

import "testing"

func TestOAuthRedirectURL(t *testing.T) {
	const pub = "https://api.example.com"
	cases := []struct {
		provider string
		want     string
	}{
		{ProviderGoogleOAuth, pub + "/api/v1/auth/oauth/google/callback"},
		{ProviderGitHubOAuth, pub + "/api/v1/auth/oauth/github/callback"},
		{ProviderAppleSignIn, pub + "/api/v1/auth/oauth/apple/callback"},
		{ProviderStripe, ""},
		{ProviderAppleIAP, ""},
	}
	for _, tc := range cases {
		if got := OAuthRedirectURL(pub+"/", tc.provider); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.provider, got, tc.want)
		}
	}
	if got := OAuthRedirectURL("  ", ProviderGitHubOAuth); got != "" {
		t.Fatalf("blank public URL: got %q", got)
	}
}
