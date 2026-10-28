package provider

import (
	"context"
	"net/http"
)

// GitHubTestProvider is a GitHub WebProvider with overridable endpoints (tests).
type GitHubTestProvider struct {
	HTTP     *http.Client
	AuthURL  string
	TokenURL string
	APIBase  string
}

func (g *GitHubTestProvider) AuthCodeURL(cfg Config, state, nonce, pkceVerifier, redirectURI string) string {
	inner := &githubProvider{http: g.HTTP, AuthURL: g.AuthURL, TokenURL: g.TokenURL, APIBase: g.APIBase}
	return inner.AuthCodeURL(cfg, state, nonce, pkceVerifier, redirectURI)
}

func (g *GitHubTestProvider) Exchange(ctx context.Context, cfg Config, code, pkceVerifier, nonce, redirectURI string) (Identity, error) {
	inner := &githubProvider{http: g.HTTP, AuthURL: g.AuthURL, TokenURL: g.TokenURL, APIBase: g.APIBase}
	return inner.Exchange(ctx, cfg, code, pkceVerifier, nonce, redirectURI)
}
