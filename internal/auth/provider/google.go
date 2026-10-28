package provider

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/builtly/builtly/internal/platform/jwks"
)

const googleJWKS = "https://www.googleapis.com/oauth2/v3/certs"

type googleProvider struct {
	http *http.Client
	jwks *jwks.Cache
	// Base overrides for tests.
	AuthURL  string
	TokenURL string
	JWKSURL  string
}

func (g *googleProvider) conf(cfg Config, redirectURI string) *oauth2.Config {
	ep := google.Endpoint
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
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     ep,
	}
}

func (g *googleProvider) AuthCodeURL(cfg Config, state, nonce, pkceVerifier, redirectURI string) string {
	c := g.conf(cfg, redirectURI)
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.S256ChallengeOption(pkceVerifier),
	}
	return c.AuthCodeURL(state, opts...)
}

func (g *googleProvider) Exchange(ctx context.Context, cfg Config, code, pkceVerifier, nonce, redirectURI string) (Identity, error) {
	c := g.conf(cfg, redirectURI)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.http)
	tok, err := c.Exchange(ctx, code, oauth2.VerifierOption(pkceVerifier))
	if err != nil {
		return Identity{}, fmt.Errorf("google exchange: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, fmt.Errorf("google: missing id_token")
	}
	return g.verify(ctx, cfg, raw, nonce, []string{cfg.ClientID})
}

func (g *googleProvider) VerifyIDToken(ctx context.Context, cfg Config, idToken, nonce string) (Identity, error) {
	auds := append([]string{}, cfg.NativeClientIDs...)
	if cfg.ClientID != "" {
		auds = append(auds, cfg.ClientID)
	}
	return g.verify(ctx, cfg, idToken, nonce, auds)
}

func (g *googleProvider) verify(ctx context.Context, cfg Config, idToken, nonce string, audiences []string) (Identity, error) {
	url := g.JWKSURL
	if url == "" {
		url = googleJWKS
	}
	tok, err := g.jwks.VerifyRS256(ctx, url, idToken,
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return Identity{}, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return Identity{}, fmt.Errorf("google: bad claims")
	}
	iss, _ := claims["iss"].(string)
	if iss != "accounts.google.com" && iss != "https://accounts.google.com" {
		return Identity{}, fmt.Errorf("google: bad iss")
	}
	audOK := false
	switch a := claims["aud"].(type) {
	case string:
		audOK = slices.Contains(audiences, a)
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && slices.Contains(audiences, s) {
				audOK = true
				break
			}
		}
	}
	if !audOK {
		return Identity{}, fmt.Errorf("google: bad aud")
	}
	if nonce != "" {
		if n, _ := claims["nonce"].(string); n != nonce {
			return Identity{}, fmt.Errorf("google: nonce mismatch")
		}
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	ev, _ := claims["email_verified"].(bool)
	// Google sometimes encodes email_verified as string
	if !ev {
		if s, ok := claims["email_verified"].(string); ok && s == "true" {
			ev = true
		}
	}
	name, _ := claims["name"].(string)
	pic, _ := claims["picture"].(string)
	if sub == "" {
		return Identity{}, fmt.Errorf("google: missing sub")
	}
	return Identity{
		Provider: Google, Subject: sub, Email: email, EmailVerified: ev,
		Name: name, AvatarURL: pic,
	}, nil
}
