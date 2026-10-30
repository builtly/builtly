package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/tenant"
)

// OAuthDeps are social-auth dependencies injected after construction.
type OAuthDeps struct {
	Providers *provider.Registry
	Resolver  *integration.Resolver
	Box       *secrets.Box
	PublicURL string
	Tenants   *tenant.Service
}

// SetOAuth wires social auth dependencies.
func (s *Service) SetOAuth(d OAuthDeps) { s.oauth = d }

// StartOAuth validates input and returns the provider authorize URL.
func (s *Service) StartOAuth(ctx context.Context, providerName string, appID uuid.UUID, redirectURI, codeChallenge, codeChallengeMethod string, attribution json.RawMessage, referralCode string) (authorizeURL string, err error) {
	if codeChallengeMethod != "" && codeChallengeMethod != "S256" {
		return "", ErrInvalidOAuthRequest
	}
	if codeChallenge == "" {
		return "", ErrInvalidOAuthRequest
	}
	web, ok := s.oauth.Providers.Web(providerName)
	if !ok {
		return "", ErrInvalidOAuthRequest
	}
	app, err := s.oauth.Tenants.Get(ctx, appID)
	if err != nil {
		return "", err
	}
	if !providerEnabled(app, providerName) {
		return "", ErrProviderDisabled
	}
	if !redirectAllowed(app, redirectURI) {
		return "", ErrInvalidRedirectURI
	}
	integName := provider.IntegrationProvider(providerName)
	resolved, err := s.oauth.Resolver.For(ctx, appID, integName)
	if err == integration.ErrNotConfigured {
		return "", ErrIntegrationNotConfigured
	}
	if err != nil {
		return "", err
	}
	cfg, err := provider.ParseConfig(integName, resolved.Settings, resolved.Secrets)
	if err != nil {
		return "", err
	}
	state := provider.NewState()
	nonce := provider.NewState()
	provVerifier, err := randomVerifier()
	if err != nil {
		return "", err
	}
	flowID := uuid.Must(uuid.NewV7())
	enc, err := s.oauth.Box.SealEmbedded([]byte(provVerifier), []byte(flowID.String()))
	if err != nil {
		return "", err
	}
	attr := attribution
	if len(attr) == 0 {
		attr = json.RawMessage(`{}`)
	}
	var ref *string
	if referralCode != "" {
		ref = &referralCode
	}
	now := db.Now()
	flow := OAuthFlow{
		ID: flowID, AppID: appID, Provider: providerName, ConnectionID: resolved.ConnectionID,
		StateHash: hashToken(state), RedirectURI: redirectURI, ClientCodeChallenge: codeChallenge,
		ProviderVerifierEnc: enc, Nonce: nonce, Attribution: attr, ReferralCode: ref,
		Status: FlowStarted, ExpiresAt: db.Time(time.Now().UTC().Add(10 * time.Minute)), CreatedAt: now,
	}
	if err := s.store.InsertOAuthFlow(ctx, nil, flow); err != nil {
		return "", err
	}
	callback := integration.OAuthRedirectURL(s.oauth.PublicURL, provider.IntegrationProvider(providerName))
	return web.AuthCodeURL(cfg, state, nonce, provVerifier, callback), nil
}

// HandleOAuthCallback completes provider exchange and returns redirect to client.
func (s *Service) HandleOAuthCallback(ctx context.Context, providerName, code, state, errCode string, appleUserName string) (redirect string, err error) {
	if state == "" {
		return "", ErrInvalidOAuthRequest
	}
	flow, err := s.store.GetOAuthFlowByStateHash(ctx, nil, hashToken(state))
	if err != nil {
		return "", ErrInvalidOAuthRequest
	}
	if flow.Provider != providerName || flow.Status != FlowStarted {
		return "", ErrInvalidOAuthRequest
	}
	if !flow.ExpiresAt.Time().After(time.Now().UTC()) {
		_ = s.store.FailOAuthFlow(ctx, nil, flow.ID)
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	if errCode != "" {
		_ = s.store.FailOAuthFlow(ctx, nil, flow.ID)
		return oauthErrRedirect(flow.RedirectURI, errCode), nil
	}
	if code == "" {
		_ = s.store.FailOAuthFlow(ctx, nil, flow.ID)
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}

	web, ok := s.oauth.Providers.Web(providerName)
	if !ok {
		return "", ErrInvalidOAuthRequest
	}
	integName := provider.IntegrationProvider(providerName)
	resolved, err := s.oauth.Resolver.For(ctx, flow.AppID, integName)
	if err != nil {
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	cfg, err := provider.ParseConfig(integName, resolved.Settings, resolved.Secrets)
	if err != nil {
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	provVerifier, err := s.oauth.Box.OpenEmbedded(flow.ProviderVerifierEnc, []byte(flow.ID.String()))
	if err != nil {
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	callback := integration.OAuthRedirectURL(s.oauth.PublicURL, provider.IntegrationProvider(providerName))
	ident, err := web.Exchange(ctx, cfg, code, string(provVerifier), flow.Nonce, callback)
	if err != nil {
		_ = s.store.FailOAuthFlow(ctx, nil, flow.ID)
		if provider.IsEmailRequired(err) {
			return oauthErrRedirect(flow.RedirectURI, "email_required"), nil
		}
		slog.Warn("oauth provider exchange failed", "provider", providerName, "app_id", flow.AppID, "err", err)
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	if appleUserName != "" && ident.Name == "" {
		ident.Name = appleUserName
	}

	app, err := s.oauth.Tenants.Get(ctx, flow.AppID)
	if err != nil {
		slog.Warn("oauth app load failed", "provider", providerName, "app_id", flow.AppID, "err", err)
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	user, isNew, err := s.resolveIdentity(ctx, app, ident, flow.Attribution, flow.ReferralCode)
	if err != nil {
		_ = s.store.FailOAuthFlow(ctx, nil, flow.ID)
		if errors.Is(err, ErrAccountDisabled) {
			return oauthErrRedirect(flow.RedirectURI, "account_disabled"), nil
		}
		if errors.Is(err, ErrEmailRequired) {
			return oauthErrRedirect(flow.RedirectURI, "email_required"), nil
		}
		slog.Warn("oauth resolve identity failed", "provider", providerName, "app_id", flow.AppID, "err", err)
		return oauthErrRedirect(flow.RedirectURI, "access_denied"), nil
	}
	_ = isNew

	loginRaw, loginHash, err := newOpaqueToken("lc_")
	if err != nil {
		return "", err
	}
	if err := s.store.AuthenticateOAuthFlow(ctx, nil, flow.ID, user.ID, loginHash, db.Now()); err != nil {
		return "", err
	}
	return oauthCodeRedirect(flow.RedirectURI, loginRaw), nil
}

// ExchangeLoginCode verifies client PKCE and issues a session.
func (s *Service) ExchangeLoginCode(ctx context.Context, app tenant.App, loginCode, codeVerifier, ua, ip string) (TokenResponse, error) {
	if !strings.HasPrefix(loginCode, "lc_") || codeVerifier == "" {
		return TokenResponse{}, ErrInvalidToken
	}
	flow, err := s.store.GetOAuthFlowByLoginCodeHash(ctx, nil, hashToken(loginCode))
	if err != nil {
		return TokenResponse{}, ErrInvalidToken
	}
	if flow.AppID != app.ID || flow.Status != FlowAuthenticated {
		return TokenResponse{}, ErrInvalidToken
	}
	if !flow.ExpiresAt.Time().After(time.Now().UTC()) {
		return TokenResponse{}, ErrInvalidToken
	}
	if !verifyS256(codeVerifier, flow.ClientCodeChallenge) {
		return TokenResponse{}, ErrInvalidToken
	}
	if err := s.store.MarkOAuthFlowExchanged(ctx, nil, flow.ID); err != nil {
		return TokenResponse{}, ErrInvalidToken
	}
	if flow.UserID == nil {
		return TokenResponse{}, ErrInvalidToken
	}
	u, err := s.store.GetByID(ctx, nil, flow.AppID, *flow.UserID)
	if err != nil || u.Status != StatusActive {
		return TokenResponse{}, ErrInvalidToken
	}
	method := flow.Provider
	if method == provider.GitHub {
		method = MethodGitHub
	}
	return s.issueSession(ctx, app, u, method, ua, ip, false)
}

// NativeApple handles POST /auth/apple.
func (s *Service) NativeApple(ctx context.Context, app tenant.App, identityToken, authCode, rawNonce string, fullName string, attribution json.RawMessage, referral string, ua, ip string) (TokenResponse, error) {
	if !providerEnabled(app, provider.Apple) {
		return TokenResponse{}, ErrProviderDisabled
	}
	resolved, err := s.oauth.Resolver.For(ctx, app.ID, integration.ProviderAppleSignIn)
	if err == integration.ErrNotConfigured {
		return TokenResponse{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return TokenResponse{}, err
	}
	cfg, err := provider.ParseConfig(integration.ProviderAppleSignIn, resolved.Settings, resolved.Secrets)
	if err != nil {
		return TokenResponse{}, err
	}
	nonce := ""
	if rawNonce != "" {
		nonce = provider.NonceHash(rawNonce)
	}
	ident, err := s.oauth.Providers.Native.Apple.VerifyIDToken(ctx, cfg, identityToken, nonce)
	if err != nil {
		return TokenResponse{}, ErrInvalidToken
	}
	if fullName != "" && ident.Name == "" {
		ident.Name = fullName
	}
	if authCode != "" {
		if rt, err := exchangeAppleNative(ctx, s, cfg, authCode); err == nil {
			ident.RefreshToken = rt
		}
	}
	user, isNew, err := s.resolveIdentity(ctx, app, ident, attribution, strPtr(referral))
	if err != nil {
		return TokenResponse{}, err
	}
	return s.issueSession(ctx, app, user, MethodApple, ua, ip, isNew)
}

// NativeGoogle handles POST /auth/google.
func (s *Service) NativeGoogle(ctx context.Context, app tenant.App, idToken, rawNonce string, attribution json.RawMessage, referral, ua, ip string) (TokenResponse, error) {
	if !providerEnabled(app, provider.Google) {
		return TokenResponse{}, ErrProviderDisabled
	}
	resolved, err := s.oauth.Resolver.For(ctx, app.ID, integration.ProviderGoogleOAuth)
	if err == integration.ErrNotConfigured {
		return TokenResponse{}, ErrIntegrationNotConfigured
	}
	if err != nil {
		return TokenResponse{}, err
	}
	cfg, err := provider.ParseConfig(integration.ProviderGoogleOAuth, resolved.Settings, resolved.Secrets)
	if err != nil {
		return TokenResponse{}, err
	}
	nonce := rawNonce
	ident, err := s.oauth.Providers.Native.Google.VerifyIDToken(ctx, cfg, idToken, nonce)
	if err != nil {
		return TokenResponse{}, ErrInvalidToken
	}
	user, isNew, err := s.resolveIdentity(ctx, app, ident, attribution, strPtr(referral))
	if err != nil {
		return TokenResponse{}, err
	}
	return s.issueSession(ctx, app, user, MethodGoogle, ua, ip, isNew)
}

func exchangeAppleNative(ctx context.Context, s *Service, cfg provider.Config, code string) (string, error) {
	bundleID := ""
	if len(cfg.BundleIDs) > 0 {
		bundleID = cfg.BundleIDs[0]
	}
	type exchanger interface {
		ExchangeNativeCode(ctx context.Context, cfg provider.Config, code, bundleID string) (string, error)
	}
	if ex, ok := s.oauth.Providers.Apple.(exchanger); ok {
		return ex.ExchangeNativeCode(ctx, cfg, code, bundleID)
	}
	return "", fmt.Errorf("unsupported")
}

func (s *Service) resolveIdentity(ctx context.Context, app tenant.App, ident provider.Identity, attribution json.RawMessage, referralCode *string) (User, bool, error) {
	var out User
	var isNew bool
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		existing, err := s.store.GetIdentityBySubject(ctx, tx, app.ID, ident.Provider, ident.Subject)
		if err == nil {
			u, err := s.store.GetByID(ctx, tx, app.ID, existing.UserID)
			if err != nil {
				return err
			}
			if u.Status == StatusDeleted || u.Status == StatusDisabled {
				return ErrAccountDisabled
			}
			var refreshEnc []byte
			if ident.RefreshToken != "" {
				refreshEnc, _ = s.oauth.Box.SealEmbedded([]byte(ident.RefreshToken), []byte(existing.ID.String()))
			}
			_ = s.store.TouchIdentity(ctx, tx, app.ID, existing.ID, refreshEnc, now)
			if ident.Name != "" {
				_ = s.store.SetDisplayNameIfEmpty(ctx, tx, app.ID, u.ID, ident.Name, now)
			}
			out = u
			return nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		if !ident.EmailVerified || ident.Email == "" {
			return ErrEmailRequired
		}
		emailNorm := strings.ToLower(ident.Email)
		u, err := s.store.GetByEmail(ctx, tx, app.ID, emailNorm)
		if err == nil {
			if !u.EmailVerifiedAt.Valid {
				if err := s.store.TakeoverUnverifiedPassword(ctx, tx, app.ID, u.ID, now); err != nil {
					return err
				}
				_ = s.store.RevokeAllRefreshForUser(ctx, tx, app.ID, u.ID, now)
				u.PasswordHash = nil
				u.EmailVerifiedAt = db.NullTime{Time: now, Valid: true}
			}
			if err := s.linkIdentity(ctx, tx, app.ID, u.ID, ident, now); err != nil {
				return err
			}
			if ident.Name != "" {
				_ = s.store.SetDisplayNameIfEmpty(ctx, tx, app.ID, u.ID, ident.Name, now)
			}
			out = u
			return nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return err
		}

		// Create new user
		attr := attribution
		if len(attr) == 0 {
			attr = json.RawMessage(`{}`)
		}
		uid := uuid.Must(uuid.NewV7())
		ownCode, err := referral.GenerateCode()
		if err != nil {
			return err
		}
		u = User{
			ID: uid, AppID: app.ID, Email: &emailNorm,
			EmailVerifiedAt: db.NullTime{Time: now, Valid: true},
			DisplayName:     ident.Name, AvatarURL: ident.AvatarURL,
			Status: StatusActive, Attribution: attr, ReferralCode: &ownCode,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.Insert(ctx, tx, u); err != nil {
			return err
		}
		if app.Settings.SignupBonusCredits > 0 {
			if _, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
				AppID: app.ID, UserID: uid, EntryType: credit.TypeSignupBonus,
				Amount:         app.Settings.SignupBonusCredits,
				IdempotencyKey: "signup:" + uid.String(), Actor: "system",
			}); err != nil {
				return err
			}
		}
		if err := s.linkIdentity(ctx, tx, app.ID, uid, ident, now); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"password_signup": false,
			"to":              emailNorm,
			"provider":        ident.Provider,
		})
		appID := app.ID
		_ = s.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appID, Type: outbox.EventUserRegistered, Payload: payload,
		})
		if err := s.publishWelcome(ctx, tx, app, emailNorm); err != nil {
			return err
		}
		if referralCode != nil && *referralCode != "" && s.referrals != nil {
			_, _ = s.referrals.ApplyTx(ctx, tx, app, uid, *referralCode)
		}
		out = u
		isNew = true
		return nil
	})
	return out, isNew, err
}

func (s *Service) linkIdentity(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID, ident provider.Identity, now db.Time) error {
	id := uuid.Must(uuid.NewV7())
	var refreshEnc []byte
	if ident.RefreshToken != "" {
		var err error
		refreshEnc, err = s.oauth.Box.SealEmbedded([]byte(ident.RefreshToken), []byte(id.String()))
		if err != nil {
			return err
		}
	}
	var email *string
	if ident.Email != "" {
		e := strings.ToLower(ident.Email)
		email = &e
	}
	return s.store.InsertIdentity(ctx, tx, Identity{
		ID: id, AppID: appID, UserID: userID, Provider: ident.Provider,
		ProviderSubject: ident.Subject, Email: email, EmailVerified: ident.EmailVerified,
		ProviderRefreshEnc: refreshEnc, CreatedAt: now, LastUsedAt: now,
	})
}

func providerEnabled(app tenant.App, name string) bool {
	for _, p := range app.Settings.Auth.Providers {
		if p == name {
			return true
		}
	}
	return false
}

func redirectAllowed(app tenant.App, redirectURI string) bool {
	for _, u := range app.Settings.AuthRedirectURLs {
		if u == redirectURI {
			return true
		}
	}
	return false
}

func oauthErrRedirect(redirectURI, code string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	q.Set("error", code)
	u.RawQuery = q.Encode()
	return u.String()
}

func oauthCodeRedirect(redirectURI, code string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	q := u.Query()
	q.Set("code", code)
	u.RawQuery = q.Encode()
	return u.String()
}

func verifyS256(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return got == challenge
}

func randomVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
