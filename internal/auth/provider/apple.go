package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/jwks"
)

const (
	appleJWKS      = "https://appleid.apple.com/auth/keys"
	appleAuthURL   = "https://appleid.apple.com/auth/authorize"
	appleTokenURL  = "https://appleid.apple.com/auth/token"
	appleRevokeURL = "https://appleid.apple.com/auth/revoke"
)

type appleProvider struct {
	http      *http.Client
	jwks      *jwks.Cache
	AuthURL   string
	TokenURL  string
	RevokeURL string
	JWKSURL   string

	mu        sync.Mutex
	secret    string
	secretExp time.Time
	secretKey string
}

func (a *appleProvider) AuthCodeURL(cfg Config, state, nonce, pkceVerifier, redirectURI string) string {
	base := a.AuthURL
	if base == "" {
		base = appleAuthURL
	}
	challenge := s256Challenge(pkceVerifier)
	q := url.Values{}
	q.Set("client_id", cfg.ServicesID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("response_mode", "form_post")
	q.Set("scope", "name email")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return base + "?" + q.Encode()
}

func (a *appleProvider) Exchange(ctx context.Context, cfg Config, code, pkceVerifier, nonce, redirectURI string) (Identity, error) {
	secret, err := a.clientSecret(cfg, cfg.ServicesID)
	if err != nil {
		return Identity{}, err
	}
	tokenURL := a.TokenURL
	if tokenURL == "" {
		tokenURL = appleTokenURL
	}
	form := url.Values{}
	form.Set("client_id", cfg.ServicesID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", pkceVerifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("apple token: %s", truncate(string(body), 200))
	}
	var tok struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return Identity{}, err
	}
	id, err := a.verify(ctx, tok.IDToken, nonce, []string{cfg.ServicesID})
	if err != nil {
		return Identity{}, err
	}
	id.RefreshToken = tok.RefreshToken
	return id, nil
}

func (a *appleProvider) VerifyIDToken(ctx context.Context, cfg Config, idToken, nonce string) (Identity, error) {
	return a.verify(ctx, idToken, nonce, cfg.BundleIDs)
}

// ExchangeNativeCode exchanges authorization_code for Apple refresh token (native).
func (a *appleProvider) ExchangeNativeCode(ctx context.Context, cfg Config, code, bundleID string) (string, error) {
	secret, err := a.clientSecret(cfg, bundleID)
	if err != nil {
		return "", err
	}
	tokenURL := a.TokenURL
	if tokenURL == "" {
		tokenURL = appleTokenURL
	}
	form := url.Values{}
	form.Set("client_id", bundleID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("apple token: %s", truncate(string(body), 200))
	}
	var tok struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal(body, &tok)
	return tok.RefreshToken, nil
}

func (a *appleProvider) Revoke(ctx context.Context, cfg Config, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	clientID := cfg.ServicesID
	if clientID == "" && len(cfg.BundleIDs) > 0 {
		clientID = cfg.BundleIDs[0]
	}
	secret, err := a.clientSecret(cfg, clientID)
	if err != nil {
		return err
	}
	revokeURL := a.RevokeURL
	if revokeURL == "" {
		revokeURL = appleRevokeURL
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("apple revoke: %d %s", res.StatusCode, truncate(string(b), 200))
	}
	return nil
}

func (a *appleProvider) verify(ctx context.Context, idToken, nonce string, audiences []string) (Identity, error) {
	jwksURL := a.JWKSURL
	if jwksURL == "" {
		jwksURL = appleJWKS
	}
	tok, err := a.jwks.VerifyRS256(ctx, jwksURL, idToken,
		jwt.WithExpirationRequired(),
		jwt.WithIssuer("https://appleid.apple.com"),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return Identity{}, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return Identity{}, fmt.Errorf("apple: bad claims")
	}
	audOK := false
	switch aud := claims["aud"].(type) {
	case string:
		audOK = slices.Contains(audiences, aud)
	case []any:
		for _, x := range aud {
			if s, ok := x.(string); ok && slices.Contains(audiences, s) {
				audOK = true
				break
			}
		}
	}
	if !audOK {
		return Identity{}, fmt.Errorf("apple: bad aud")
	}
	if nonce != "" {
		if n, _ := claims["nonce"].(string); n != nonce {
			return Identity{}, fmt.Errorf("apple: nonce mismatch")
		}
	}
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	ev := email != ""
	if sub == "" {
		return Identity{}, fmt.Errorf("apple: missing sub")
	}
	return Identity{Provider: Apple, Subject: sub, Email: email, EmailVerified: ev}, nil
}

func (a *appleProvider) clientSecret(cfg Config, subject string) (string, error) {
	cacheKey := cfg.TeamID + "|" + cfg.KeyID + "|" + subject
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.secret != "" && a.secretKey == cacheKey && time.Now().Before(a.secretExp) {
		return a.secret, nil
	}
	key, err := parseECPrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": cfg.TeamID,
		"iat": now.Unix(),
		"exp": now.Add(24 * time.Hour).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": subject,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = cfg.KeyID
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", err
	}
	a.secret = signed
	a.secretKey = cacheKey
	a.secretExp = now.Add(20 * time.Hour)
	return signed, nil
}

func parseECPrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("apple: invalid PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ec, ok := key.(*ecdsa.PrivateKey); ok {
			return ec, nil
		}
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NonceHash returns hex(SHA256(raw)) as Apple native expects.
func NonceHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewState returns a random opaque state string.
func NewState() string {
	return uuid.Must(uuid.NewV7()).String() + uuid.Must(uuid.NewV7()).String()
}
