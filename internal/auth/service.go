package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/tenant"
)

// FileMarker marks a user's files for purge during account deletion (storage).
type FileMarker interface {
	MarkDeletedForUser(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error
}

// AvatarUploader reserves and completes avatar uploads to object storage (R2).
type AvatarUploader interface {
	BeginAvatar(ctx context.Context, app tenant.App, userID uuid.UUID, filename, contentType string, sizeBytes int64) (AvatarUploadBegin, error)
	// FinishAvatar verifies the object and calls bind in the same DB transaction as marking the file uploaded.
	FinishAvatar(ctx context.Context, app tenant.App, userID, fileID uuid.UUID, bind func(context.Context, db.Querier, string) error) (publicURL string, err error)
}

// AvatarUploadBegin is the create-avatar response payload.
type AvatarUploadBegin struct {
	FileID      uuid.UUID
	Filename    string
	ContentType string
	SizeBytes   int64
	Upload      struct {
		Method    string
		URL       string
		Headers   map[string]string
		ExpiresAt time.Time
	}
}

// ReferralApplier applies referral codes at sign-up / deletion void.
type ReferralApplier interface {
	ApplyTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, refereeID uuid.UUID, code string) (referral.ApplyResult, error)
	VoidAsReferee(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error
}

// Service implements password auth and session lifecycle.
type Service struct {
	store      *Store
	db         *db.DB
	tokens     *TokenService
	credits    *credit.Service
	outbox     *outbox.Publisher
	refreshTTL time.Duration
	oauth      OAuthDeps
	files      FileMarker
	avatars    AvatarUploader
	referrals  ReferralApplier
	welcome    WelcomeMailer
}

// WelcomeMailer enqueues an optional post-registration mail inside the caller's transaction.
type WelcomeMailer interface {
	PublishWelcomeTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, to string) error
}

// SetWelcomeMailer attaches the optional welcome-mail publisher.
func (s *Service) SetWelcomeMailer(m WelcomeMailer) { s.welcome = m }

// Store exposes the auth store.
func (s *Service) Store() *Store { return s.store }

// Tokens exposes the token service.
func (s *Service) Tokens() *TokenService { return s.tokens }

// NewService constructs an auth Service.
func NewService(
	database *db.DB,
	store *Store,
	tokens *TokenService,
	credits *credit.Service,
	pub *outbox.Publisher,
	refreshTTL time.Duration,
) *Service {
	if refreshTTL <= 0 {
		refreshTTL = 720 * time.Hour
	}
	return &Service{
		store: store, db: database, tokens: tokens,
		credits: credits, outbox: pub, refreshTTL: refreshTTL,
	}
}

// RegisterInput is POST /auth/register.
type RegisterInput struct {
	Email        string
	Password     string
	DisplayName  string
	Locale       string
	ReferralCode string
	Attribution  json.RawMessage
	UserAgent    string
	IP           string
}

// LoginInput is POST /auth/login.
type LoginInput struct {
	Email     string
	Password  string
	UserAgent string
	IP        string
}

// Register creates a password user and session.
// Guest emails matching auth.guest_email_domain are allowed when guest login is enabled, even when regular password login is disabled.
func (s *Service) Register(ctx context.Context, app tenant.App, in RegisterInput) (TokenResponse, error) {
	emailAddr, err := normalizeEmail(in.Email)
	if err != nil {
		return TokenResponse{}, err
	}
	isGuest := app.Settings.IsGuestEmail(emailAddr)
	if !app.Settings.Auth.PasswordEnabled && !isGuest {
		return TokenResponse{}, ErrPasswordDisabled
	}
	if err := ValidatePasswordLength(in.Password); err != nil {
		return TokenResponse{}, err
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return TokenResponse{}, err
	}
	attr := in.Attribution
	if len(attr) == 0 {
		attr = json.RawMessage(`{}`)
	}

	var resp TokenResponse
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		uid := uuid.Must(uuid.NewV7())
		code, err := referral.GenerateCode()
		if err != nil {
			return err
		}
		u := User{
			ID: uid, AppID: app.ID, Email: &emailAddr, PasswordHash: &hash,
			DisplayName: in.DisplayName, Locale: NormalizeLocale(in.Locale), LastIP: in.IP,
			Status: StatusActive, Attribution: attr, ReferralCode: &code,
			CreatedAt: now, UpdatedAt: now,
		}
		if isGuest {
			// Guest accounts have verified email automatically (no email token/verification needed)
			u.EmailVerifiedAt = db.NullTime{Time: now, Valid: true}
		}
		if err := s.store.Insert(ctx, tx, u); err != nil {
			var uv db.ErrUniqueViolation
			if errors.As(err, &uv) {
				return ErrEmailTaken
			}
			return err
		}
		if !isGuest && app.Settings.SignupBonusCredits > 0 {
			if _, err := s.credits.ApplyTx(ctx, tx, credit.ApplyInput{
				AppID: app.ID, UserID: uid, EntryType: credit.TypeSignupBonus,
				Amount:         app.Settings.SignupBonusCredits,
				IdempotencyKey: "signup:" + uid.String(),
				Actor:          "system",
			}); err != nil {
				return err
			}
		}

		if !isGuest {
			verifyRaw, verifyHash, err := newEmailTokenRaw()
			if err != nil {
				return err
			}
			et := EmailToken{
				ID: uuid.Must(uuid.NewV7()), AppID: app.ID, UserID: uid,
				Purpose: PurposeVerifyEmail, Email: emailAddr, TokenHash: verifyHash,
				ExpiresAt: db.Time(time.Now().UTC().Add(24 * time.Hour)), CreatedAt: now,
			}
			if err := s.store.InsertEmailToken(ctx, tx, et); err != nil {
				return err
			}

			payload, err := buildUserRegisteredPayload(app, uid, emailAddr, verifyRaw, et.ExpiresAt.Time())
			if err != nil {
				return err
			}
			appID := app.ID
			if err := s.outbox.PublishTx(ctx, tx, outbox.Event{
				AppID: &appID, Type: outbox.EventUserRegistered, Payload: payload,
			}); err != nil {
				return err
			}
			if err := s.publishWelcome(ctx, tx, app, emailAddr); err != nil {
				return err
			}
		}

		tr, err := s.issueSessionTx(ctx, tx, app, u, MethodPassword, in.UserAgent, in.IP, true)
		if err != nil {
			return err
		}
		if !isGuest && in.ReferralCode != "" && s.referrals != nil {
			ar, err := s.referrals.ApplyTx(ctx, tx, app, uid, in.ReferralCode)
			if err != nil {
				return err
			}
			tr.Referral = &ReferralSignupResult{Applied: ar.Applied, Reason: ar.Reason}
		}
		resp = tr
		return nil
	})
	return resp, err
}

// Login authenticates email/password.
// Guest emails matching auth.guest_email_domain are allowed when guest login is enabled, even when regular password login is disabled.
func (s *Service) Login(ctx context.Context, app tenant.App, in LoginInput) (TokenResponse, error) {
	emailAddr, err := normalizeEmail(in.Email)
	if err != nil {
		// still burn cycles
		_, _ = VerifyPassword(in.Password, dummyHash)
		return TokenResponse{}, ErrInvalidCredentials
	}
	if !app.Settings.Auth.PasswordEnabled && !app.Settings.IsGuestEmail(emailAddr) {
		return TokenResponse{}, ErrPasswordDisabled
	}
	u, err := s.store.GetByEmail(ctx, nil, app.ID, emailAddr)
	if err != nil {
		_, _ = VerifyPassword(in.Password, dummyHash)
		return TokenResponse{}, ErrInvalidCredentials
	}
	if u.Status == StatusDisabled || u.Status == StatusDeleted {
		return TokenResponse{}, ErrAccountDisabled
	}
	if u.PasswordHash == nil || *u.PasswordHash == "" {
		_, _ = VerifyPassword(in.Password, dummyHash)
		return TokenResponse{}, ErrInvalidCredentials
	}
	ok, needsRehash := VerifyPassword(in.Password, *u.PasswordHash)
	if !ok {
		return TokenResponse{}, ErrInvalidCredentials
	}
	if app.Settings.Auth.RequireEmailVerification && !u.EmailVerifiedAt.Valid {
		return TokenResponse{}, ErrEmailNotVerified
	}
	if needsRehash {
		if nh, err := HashPassword(in.Password); err == nil {
			_ = s.store.UpdatePasswordHash(ctx, nil, app.ID, u.ID, nh, db.Now())
		}
	}
	return s.issueSession(ctx, app, u, MethodPassword, in.UserAgent, in.IP, false)
}

// Refresh rotates a refresh token.
func (s *Service) Refresh(ctx context.Context, app tenant.App, raw, userAgent, ip string) (TokenResponse, error) {
	if !strings.HasPrefix(raw, "rt_") {
		return TokenResponse{}, ErrInvalidToken
	}
	hash := hashToken(raw)
	rt, err := s.store.GetRefreshByHash(ctx, nil, hash)
	if err != nil {
		return TokenResponse{}, ErrInvalidToken
	}
	if rt.AppID != app.ID {
		return TokenResponse{}, ErrInvalidToken
	}
	now := time.Now().UTC()
	if rt.RevokedAt.Valid {
		// Commit family revoke outside a failing tx so reuse detection sticks.
		_ = s.store.RevokeRefreshFamily(ctx, nil, rt.AppID, rt.FamilyID, db.Time(now))
		return TokenResponse{}, ErrInvalidToken
	}
	if !rt.ExpiresAt.Time().After(now) {
		return TokenResponse{}, ErrInvalidToken
	}

	var resp TokenResponse
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// Re-load under tx to detect concurrent rotation/reuse.
		cur, err := s.store.GetRefreshByHash(ctx, tx, hash)
		if err != nil {
			return ErrInvalidToken
		}
		if cur.RevokedAt.Valid {
			return errReuseDetected{AppID: cur.AppID, FamilyID: cur.FamilyID}
		}
		u, err := s.store.GetByID(ctx, tx, cur.AppID, cur.UserID)
		if err != nil {
			return ErrInvalidToken
		}
		if u.Status != StatusActive {
			return ErrAccountDisabled
		}
		newRaw, newHash, err := newRefreshToken()
		if err != nil {
			return err
		}
		newID := uuid.Must(uuid.NewV7())
		if err := s.store.MarkRefreshReplaced(ctx, tx, cur.AppID, cur.ID, newID, db.Time(now)); err != nil {
			return err
		}
		row := RefreshToken{
			ID: newID, AppID: cur.AppID, UserID: cur.UserID, FamilyID: cur.FamilyID,
			TokenHash: newHash, AuthMethod: cur.AuthMethod,
			ExpiresAt: db.Time(now.Add(s.refreshTTL)), UserAgent: userAgent, IP: ip,
			CreatedAt: db.Time(now),
		}
		if err := s.store.InsertRefresh(ctx, tx, row); err != nil {
			return err
		}
		access, accessTTL, err := s.tokens.IssueAccess(app.ID, u.ID, cur.AuthMethod, u.EmailVerifiedAt.Valid)
		if err != nil {
			return err
		}
		_ = s.store.TouchLastLogin(ctx, tx, app.ID, u.ID, ip, db.Time(now))
		resp = TokenResponse{
			User: toPublicUser(u),
			Tokens: Tokens{
				TokenType: "Bearer", AccessToken: access, ExpiresIn: int64(accessTTL.Seconds()),
				RefreshToken: newRaw, RefreshExpiresIn: int64(s.refreshTTL.Seconds()),
			},
		}
		return nil
	})
	var reuse errReuseDetected
	if errors.As(err, &reuse) {
		_ = s.store.RevokeRefreshFamily(ctx, nil, reuse.AppID, reuse.FamilyID, db.Now())
		return TokenResponse{}, ErrInvalidToken
	}
	return resp, err
}

type errReuseDetected struct {
	AppID    uuid.UUID
	FamilyID uuid.UUID
}

func (e errReuseDetected) Error() string { return "auth: refresh reuse" }

// Logout revokes a refresh token (always succeeds from caller's perspective).
func (s *Service) Logout(ctx context.Context, raw string) {
	if !strings.HasPrefix(raw, "rt_") {
		return
	}
	rt, err := s.store.GetRefreshByHash(ctx, nil, hashToken(raw))
	if err != nil {
		return
	}
	_ = s.store.RevokeRefreshFamily(ctx, nil, rt.AppID, rt.FamilyID, db.Now())
}

// RequestVerifyEmail creates a new verify token and publishes email.requested.
func (s *Service) RequestVerifyEmail(ctx context.Context, app tenant.App, userID uuid.UUID) error {
	u, err := s.store.GetByID(ctx, nil, app.ID, userID)
	if err != nil {
		return ErrUserNotFound
	}
	if u.EmailVerifiedAt.Valid || u.Email == nil {
		return nil
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		raw, hash, err := newEmailTokenRaw()
		if err != nil {
			return err
		}
		exp := time.Now().UTC().Add(24 * time.Hour)
		et := EmailToken{
			ID: uuid.Must(uuid.NewV7()), AppID: app.ID, UserID: userID,
			Purpose: PurposeVerifyEmail, Email: *u.Email, TokenHash: hash,
			ExpiresAt: db.Time(exp), CreatedAt: now,
		}
		if err := s.store.InsertEmailToken(ctx, tx, et); err != nil {
			return err
		}
		payload, err := email.BuildVerifyPayload(*u.Email, actionURL(app, "verify", raw), exp)
		if err != nil {
			return err
		}
		appID := app.ID
		return s.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appID, Type: outbox.EventEmailRequested, Payload: payload,
		})
	})
}

// VerifyEmail consumes a verify token.
func (s *Service) VerifyEmail(ctx context.Context, app tenant.App, raw string) (PublicUser, error) {
	et, err := s.loadEmailToken(ctx, app.ID, raw, PurposeVerifyEmail)
	if err != nil {
		return PublicUser{}, err
	}
	err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		if err := s.store.ConsumeEmailToken(ctx, tx, et.AppID, et.ID, now); err != nil {
			return ErrInvalidToken
		}
		return s.store.MarkEmailVerified(ctx, tx, et.AppID, et.UserID, now)
	})
	if err != nil {
		return PublicUser{}, err
	}
	u, err := s.store.GetByID(ctx, nil, et.AppID, et.UserID)
	if err != nil {
		return PublicUser{}, err
	}
	return toPublicUser(u), nil
}

// ForgotPassword always succeeds; may enqueue reset mail.
func (s *Service) ForgotPassword(ctx context.Context, app tenant.App, emailAddr string) error {
	normalized, err := normalizeEmail(emailAddr)
	if err != nil || app.Settings.IsGuestEmail(normalized) {
		return nil
	}
	u, err := s.store.GetByEmail(ctx, nil, app.ID, normalized)
	if err != nil {
		return nil
	}
	if u.Status != StatusActive || u.PasswordHash == nil {
		return nil
	}
	_ = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		raw, hash, err := newEmailTokenRaw()
		if err != nil {
			return err
		}
		exp := time.Now().UTC().Add(time.Hour)
		et := EmailToken{
			ID: uuid.Must(uuid.NewV7()), AppID: app.ID, UserID: u.ID,
			Purpose: PurposeResetPassword, Email: normalized, TokenHash: hash,
			ExpiresAt: db.Time(exp), CreatedAt: now,
		}
		if err := s.store.InsertEmailToken(ctx, tx, et); err != nil {
			return err
		}
		payload, err := json.Marshal(email.Payload{
			Template:  "reset_password",
			To:        normalized,
			ActionURL: actionURL(app, "reset", raw),
			Expiry:    exp.Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		appID := app.ID
		return s.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appID, Type: outbox.EventEmailRequested, Payload: payload,
		})
	})
	return nil
}

// ResetPassword sets a new password and revokes all sessions.
func (s *Service) ResetPassword(ctx context.Context, app tenant.App, raw, password string) error {
	if err := ValidatePasswordLength(password); err != nil {
		return err
	}
	et, err := s.loadEmailToken(ctx, app.ID, raw, PurposeResetPassword)
	if err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		if err := s.store.ConsumeEmailToken(ctx, tx, et.AppID, et.ID, now); err != nil {
			return ErrInvalidToken
		}
		if err := s.store.ResetPasswordAndVerify(ctx, tx, et.AppID, et.UserID, hash, now); err != nil {
			return err
		}
		_ = s.store.ConsumeEmailTokensForUserPurpose(ctx, tx, et.AppID, et.UserID, PurposeResetPassword, now)
		return s.store.RevokeAllRefreshForUser(ctx, tx, et.AppID, et.UserID, now)
	})
}

// Me returns the current user profile.
func (s *Service) Me(ctx context.Context, appID, userID uuid.UUID) (MeResponse, error) {
	u, err := s.store.GetByID(ctx, nil, appID, userID)
	if err != nil {
		return MeResponse{}, ErrUserNotFound
	}
	if u.Status == StatusDeleted {
		return MeResponse{}, ErrUserNotFound
	}
	idents, err := s.store.ListIdentities(ctx, nil, appID, userID)
	if err != nil {
		return MeResponse{}, err
	}
	out := MeResponse{
		ID: u.ID, Email: u.Email, EmailVerified: u.EmailVerifiedAt.Valid,
		DisplayName: u.DisplayName, AvatarURL: u.AvatarURL,
		Locale: u.Locale, LastIP: u.LastIP, UTMSource: attributionUTMSource(u.Attribution),
		HasPassword: u.PasswordHash != nil && *u.PasswordHash != "",
		CreatedAt:   u.CreatedAt,
		Identities:  make([]MeIdentity, 0, len(idents)),
	}
	for _, id := range idents {
		out.Identities = append(out.Identities, MeIdentity{Provider: id.Provider, Email: id.Email})
	}
	return out, nil
}

// PatchMe updates profile fields.
func (s *Service) PatchMe(ctx context.Context, appID, userID uuid.UUID, displayName, avatarURL, locale *string) (MeResponse, error) {
	if locale != nil {
		norm := NormalizeLocale(*locale)
		locale = &norm
	}
	if err := s.store.UpdateProfile(ctx, nil, appID, userID, displayName, avatarURL, locale, db.Now()); err != nil {
		if IsNotFound(err) {
			return MeResponse{}, ErrUserNotFound
		}
		return MeResponse{}, err
	}
	return s.Me(ctx, appID, userID)
}

func (s *Service) requireActiveUser(ctx context.Context, appID, userID uuid.UUID) error {
	u, err := s.store.GetByID(ctx, nil, appID, userID)
	if err != nil || u.Status == StatusDeleted {
		return ErrUserNotFound
	}
	if u.Status == StatusDisabled {
		return ErrAccountDisabled
	}
	return nil
}

// BeginAvatarUpload reserves an R2 PUT for the user's avatar.
func (s *Service) BeginAvatarUpload(ctx context.Context, app tenant.App, userID uuid.UUID, filename, contentType string, sizeBytes int64) (AvatarUploadBegin, error) {
	if s.avatars == nil {
		return AvatarUploadBegin{}, ErrIntegrationNotConfigured
	}
	if err := s.requireActiveUser(ctx, app.ID, userID); err != nil {
		return AvatarUploadBegin{}, err
	}
	return s.avatars.BeginAvatar(ctx, app, userID, filename, contentType, sizeBytes)
}

// CompleteAvatarUpload verifies the object and sets avatar_url to the public R2 URL.
func (s *Service) CompleteAvatarUpload(ctx context.Context, app tenant.App, userID, fileID uuid.UUID) (MeResponse, error) {
	if s.avatars == nil {
		return MeResponse{}, ErrIntegrationNotConfigured
	}
	if err := s.requireActiveUser(ctx, app.ID, userID); err != nil {
		return MeResponse{}, err
	}
	_, err := s.avatars.FinishAvatar(ctx, app, userID, fileID, func(ctx context.Context, q db.Querier, publicURL string) error {
		return s.store.UpdateProfile(ctx, q, app.ID, userID, nil, &publicURL, nil, db.Now())
	})
	if err != nil {
		return MeResponse{}, err
	}
	return s.Me(ctx, app.ID, userID)
}

// SetFileMarker wires storage file purge on account deletion.
func (s *Service) SetFileMarker(m FileMarker) { s.files = m }

// SetAvatarUploader wires avatar uploads to object storage.
func (s *Service) SetAvatarUploader(u AvatarUploader) { s.avatars = u }

// SetReferrals wires the referral program.
func (s *Service) SetReferrals(r ReferralApplier) { s.referrals = r }

func (s *Service) publishWelcome(ctx context.Context, tx *sqlx.Tx, app tenant.App, to string) error {
	if s.welcome == nil || to == "" {
		return nil
	}
	return s.welcome.PublishWelcomeTx(ctx, tx, app, to)
}

// DeleteMe soft-deletes the account and fans out user.deleted.
func (s *Service) DeleteMe(ctx context.Context, appID, userID uuid.UUID, password *string, accessIssuedAt time.Time) error {
	u, err := s.store.GetByID(ctx, nil, appID, userID)
	if err != nil {
		return ErrUserNotFound
	}
	hasPW := u.PasswordHash != nil && *u.PasswordHash != ""
	if hasPW && time.Since(accessIssuedAt) > 5*time.Minute {
		if password == nil {
			return ErrReauthRequired
		}
		ok, _ := VerifyPassword(*password, *u.PasswordHash)
		if !ok {
			return ErrInvalidCredentials
		}
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := db.Now()
		idents, err := s.store.ListIdentities(ctx, tx, appID, userID)
		if err != nil {
			return err
		}
		var appleTokens []string
		if s.oauth.Box != nil {
			appleTokens = CollectAppleRefreshTokens(idents, s.oauth.Box.OpenEmbedded)
		}
		if s.files != nil {
			if err := s.files.MarkDeletedForUser(ctx, tx, appID, userID); err != nil {
				return err
			}
		}
		if s.referrals != nil {
			if err := s.referrals.VoidAsReferee(ctx, tx, appID, userID); err != nil {
				return err
			}
		}
		if err := s.store.DeleteIdentities(ctx, tx, appID, userID); err != nil {
			return err
		}
		if err := s.store.SoftDelete(ctx, tx, appID, userID, now); err != nil {
			return err
		}
		if err := s.store.ConsumeAllEmailTokensForUser(ctx, tx, appID, userID, now); err != nil {
			return err
		}
		if err := s.store.RevokeAllRefreshForUser(ctx, tx, appID, userID, now); err != nil {
			return err
		}
		appIDCopy := appID
		payload, _ := json.Marshal(AppleRevokePayload{
			UserID: userID.String(), AppleRefreshTokens: appleTokens,
		})
		return s.outbox.PublishTx(ctx, tx, outbox.Event{
			AppID: &appIDCopy, Type: outbox.EventUserDeleted, Payload: payload,
		})
	})
}

// RevokeAllSessions revokes refresh tokens (admin disable / password change).
func (s *Service) RevokeAllSessions(ctx context.Context, appID, userID uuid.UUID) error {
	return s.store.RevokeAllRefreshForUser(ctx, nil, appID, userID, db.Now())
}

func (s *Service) issueSession(ctx context.Context, app tenant.App, u User, method, ua, ip string, isNew bool) (TokenResponse, error) {
	var resp TokenResponse
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var err error
		resp, err = s.issueSessionTx(ctx, tx, app, u, method, ua, ip, isNew)
		return err
	})
	return resp, err
}

func (s *Service) issueSessionTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, u User, method, ua, ip string, isNew bool) (TokenResponse, error) {
	now := time.Now().UTC()
	raw, hash, err := newRefreshToken()
	if err != nil {
		return TokenResponse{}, err
	}
	family := uuid.Must(uuid.NewV7())
	rt := RefreshToken{
		ID: uuid.Must(uuid.NewV7()), AppID: app.ID, UserID: u.ID, FamilyID: family,
		TokenHash: hash, AuthMethod: method, ExpiresAt: db.Time(now.Add(s.refreshTTL)),
		UserAgent: ua, IP: ip, CreatedAt: db.Time(now),
	}
	if err := s.store.InsertRefresh(ctx, tx, rt); err != nil {
		return TokenResponse{}, err
	}
	access, accessTTL, err := s.tokens.IssueAccess(app.ID, u.ID, method, u.EmailVerifiedAt.Valid)
	if err != nil {
		return TokenResponse{}, err
	}
	_ = s.store.TouchLastLogin(ctx, tx, app.ID, u.ID, ip, db.Time(now))
	return TokenResponse{
		User:      toPublicUser(u),
		IsNewUser: isNew,
		Tokens: Tokens{
			TokenType: "Bearer", AccessToken: access, ExpiresIn: int64(accessTTL.Seconds()),
			RefreshToken: raw, RefreshExpiresIn: int64(s.refreshTTL.Seconds()),
		},
	}, nil
}

func (s *Service) loadEmailToken(ctx context.Context, appID uuid.UUID, raw, purpose string) (EmailToken, error) {
	if !strings.HasPrefix(raw, "et_") {
		return EmailToken{}, ErrInvalidToken
	}
	et, err := s.store.GetEmailTokenByHash(ctx, nil, hashToken(raw))
	if err != nil {
		return EmailToken{}, ErrInvalidToken
	}
	if et.AppID != appID || et.Purpose != purpose || et.ConsumedAt.Valid {
		return EmailToken{}, ErrInvalidToken
	}
	if !et.ExpiresAt.Time().After(time.Now().UTC()) {
		return EmailToken{}, ErrInvalidToken
	}
	return et, nil
}

func normalizeEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 254 {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", ErrInvalidEmail
	}
	email := strings.ToLower(addr.Address)
	if !strings.Contains(email, "@") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func actionURL(app tenant.App, path, token string) string {
	base := strings.TrimRight(app.Settings.Email.ActionBaseURL, "/")
	if base == "" {
		return ""
	}
	return base + "/" + path + "?token=" + token
}

func buildUserRegisteredPayload(app tenant.App, userID uuid.UUID, to, verifyRaw string, expiry time.Time) (json.RawMessage, error) {
	p := email.Payload{
		UserID:         userID.String(),
		Template:       "verify_email",
		To:             to,
		ActionURL:      actionURL(app, "verify", verifyRaw),
		Expiry:         expiry.UTC().Format(time.RFC3339),
		PasswordSignup: true,
	}
	return json.Marshal(p)
}
