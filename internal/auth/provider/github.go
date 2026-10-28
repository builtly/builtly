package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type githubProvider struct {
	http     *http.Client
	AuthURL  string
	TokenURL string
	APIBase  string
}

func (g *githubProvider) conf(cfg Config, redirectURI string) *oauth2.Config {
	ep := github.Endpoint
	if g.AuthURL != "" {
		ep.AuthURL = g.AuthURL
	}
	if g.TokenURL != "" {
		ep.TokenURL = g.TokenURL
	}
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     ep,
	}
}

func (g *githubProvider) AuthCodeURL(cfg Config, state, nonce, pkceVerifier, redirectURI string) string {
	_ = nonce
	c := g.conf(cfg, redirectURI)
	return c.AuthCodeURL(state, oauth2.S256ChallengeOption(pkceVerifier))
}

func (g *githubProvider) Exchange(ctx context.Context, cfg Config, code, pkceVerifier, nonce, redirectURI string) (Identity, error) {
	_ = nonce
	c := g.conf(cfg, redirectURI)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.http)
	tok, err := c.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return Identity{}, fmt.Errorf("github exchange: %w", err)
	}
	api := g.APIBase
	if api == "" {
		api = "https://api.github.com"
	}
	userReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/user", nil)
	userReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	userReq.Header.Set("Accept", "application/vnd.github+json")
	userRes, err := g.http.Do(userReq)
	if err != nil {
		return Identity{}, err
	}
	defer userRes.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(userRes.Body, 1<<20))
	if userRes.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("github user: %s", truncate(string(body), 200))
	}
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return Identity{}, err
	}
	email, verified := "", false
	emReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/user/emails", nil)
	emReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	emReq.Header.Set("Accept", "application/vnd.github+json")
	emRes, err := g.http.Do(emReq)
	if err == nil {
		defer emRes.Body.Close()
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		_ = json.NewDecoder(emRes.Body).Decode(&emails)
		for _, e := range emails {
			if e.Primary && e.Verified {
				email, verified = e.Email, true
				break
			}
		}
		if !verified {
			for _, e := range emails {
				if e.Verified {
					email, verified = e.Email, true
					break
				}
			}
		}
	}
	_ = user.Email
	if !verified || email == "" {
		return Identity{}, errEmailRequired
	}
	name := user.Name
	if name == "" {
		name = user.Login
	}
	return Identity{
		Provider: GitHub, Subject: strconv.FormatInt(user.ID, 10),
		Email: email, EmailVerified: true, Name: name, AvatarURL: user.AvatarURL,
	}, nil
}

// errEmailRequired is detected by auth service.
var errEmailRequired = fmt.Errorf("email_required")

// IsEmailRequired reports whether err is email_required.
func IsEmailRequired(err error) bool {
	return err != nil && (err == errEmailRequired || strings.Contains(err.Error(), "email_required"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
