package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists users, refresh tokens, and email tokens.
type Store struct {
	db *db.DB
}

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectUser = `SELECT id, app_id, email, email_verified_at, password_hash, display_name, avatar_url,
	locale, last_ip, status, attribution, referral_code, last_login_at, deleted_at, created_at, updated_at FROM users`

// GetByID loads a user by (app_id, id).
func (s *Store) GetByID(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (User, error) {
	var u User
	err := s.q(q).GetContext(ctx, &u, s.db.Rebind(selectUser+` WHERE app_id = ? AND id = ?`), appID, userID)
	if err != nil {
		return User{}, db.MapError(err)
	}
	return u, nil
}

// GetByEmail loads a user by (app_id, email).
func (s *Store) GetByEmail(ctx context.Context, q db.Querier, appID uuid.UUID, email string) (User, error) {
	var u User
	err := s.q(q).GetContext(ctx, &u, s.db.Rebind(selectUser+` WHERE app_id = ? AND email = ?`), appID, email)
	if err != nil {
		return User{}, db.MapError(err)
	}
	return u, nil
}

// GetByReferralCode loads an active user by their own referral code.
func (s *Store) GetByReferralCode(ctx context.Context, q db.Querier, appID uuid.UUID, code string) (User, error) {
	var u User
	err := s.q(q).GetContext(ctx, &u, s.db.Rebind(selectUser+` WHERE app_id = ? AND referral_code = ?`), appID, code)
	if err != nil {
		return User{}, db.MapError(err)
	}
	return u, nil
}

// Insert creates a user row.
func (s *Store) Insert(ctx context.Context, q db.Querier, u User) error {
	attr := u.Attribution
	if len(attr) == 0 {
		attr = []byte(`{}`)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO users (
			id, app_id, email, email_verified_at, password_hash, display_name, avatar_url,
			locale, last_ip, status, attribution, referral_code, last_login_at, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		u.ID, u.AppID, u.Email, u.EmailVerifiedAt, u.PasswordHash, u.DisplayName, u.AvatarURL,
		u.Locale, u.LastIP, u.Status, attr, u.ReferralCode, u.LastLoginAt, u.DeletedAt, u.CreatedAt, u.UpdatedAt)
	return db.MapError(err)
}

func usersListWhere(appID uuid.UUID, query, status string) (where string, args []any) {
	args = []any{appID}
	where = `WHERE app_id = ?`
	if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	if query != "" {
		like := query + "%"
		where += ` AND (email LIKE ? OR CAST(id AS TEXT) LIKE ?)`
		args = append(args, like, like)
	}
	return where, args
}

// Count returns users matching optional email/id prefix and status filters.
func (s *Store) Count(ctx context.Context, q db.Querier, appID uuid.UUID, query, status string) (int, error) {
	where, args := usersListWhere(appID, query, status)
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`SELECT COUNT(*) FROM users `+where), args...)
	if err != nil {
		return 0, db.MapError(err)
	}
	return n, nil
}

// List returns users for an app with optional email/id prefix filter and keyset cursor.
// after/afterID empty means the first page; order is created_at DESC, id DESC.
func (s *Store) List(ctx context.Context, q db.Querier, appID uuid.UUID, query, status string, limit int, after db.Time, afterID uuid.UUID) ([]User, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	where, args := usersListWhere(appID, query, status)
	if afterID != uuid.Nil {
		where += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, after, after, afterID)
	}
	args = append(args, limit)
	var rows []User
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectUser+` `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ?`), args...)
	if err != nil {
		return nil, db.MapError(err)
	}
	return rows, nil
}

// UpdateStatus sets user status.
func (s *Store) UpdateStatus(ctx context.Context, q db.Querier, appID, userID uuid.UUID, status string, now db.Time) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET status = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		status, now, appID, userID)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// UpdatePasswordHash sets password hash and updated_at.
func (s *Store) UpdatePasswordHash(ctx context.Context, q db.Querier, appID, userID uuid.UUID, hash string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET password_hash = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		hash, now, appID, userID)
	return db.MapError(err)
}

// UpdateProfile patches display_name / avatar_url / locale.
func (s *Store) UpdateProfile(ctx context.Context, q db.Querier, appID, userID uuid.UUID, displayName, avatarURL, locale *string, now db.Time) error {
	u, err := s.GetByID(ctx, q, appID, userID)
	if err != nil {
		return err
	}
	if displayName != nil {
		u.DisplayName = *displayName
	}
	if avatarURL != nil {
		u.AvatarURL = *avatarURL
	}
	if locale != nil {
		u.Locale = *locale
	}
	_, err = s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET display_name = ?, avatar_url = ?, locale = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		u.DisplayName, u.AvatarURL, u.Locale, now, appID, userID)
	return db.MapError(err)
}

// MarkEmailVerified sets email_verified_at when null.
func (s *Store) MarkEmailVerified(ctx context.Context, q db.Querier, appID, userID uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET email_verified_at = COALESCE(email_verified_at, ?), updated_at = ?
		WHERE app_id = ? AND id = ?`), now, now, appID, userID)
	return db.MapError(err)
}

// TouchLastLogin updates last_login_at and last_ip.
func (s *Store) TouchLastLogin(ctx context.Context, q db.Querier, appID, userID uuid.UUID, ip string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET last_login_at = ?, last_ip = ?, updated_at = ? WHERE app_id = ? AND id = ?`),
		now, ip, now, appID, userID)
	return db.MapError(err)
}

// SoftDelete anonymizes a user (M6: without Apple revoke side-effects).
func (s *Store) SoftDelete(ctx context.Context, q db.Querier, appID, userID uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET status = ?, deleted_at = ?, email = NULL, password_hash = NULL,
			display_name = '', avatar_url = '', locale = '', last_ip = '', attribution = '{}',
			referral_code = NULL, updated_at = ?
		WHERE app_id = ? AND id = ? AND status <> ?`),
		StatusDeleted, now, now, appID, userID, StatusDeleted)
	return db.MapError(err)
}

// ListIdentities returns linked providers for a user.
func (s *Store) ListIdentities(ctx context.Context, q db.Querier, appID, userID uuid.UUID) ([]Identity, error) {
	var rows []Identity
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, user_id, provider, provider_subject, email, email_verified,
			provider_refresh_enc, created_at, last_used_at
		FROM user_identities WHERE app_id = ? AND user_id = ?`), appID, userID)
	return rows, db.MapError(err)
}

// DeleteIdentities removes all identities for a user.
func (s *Store) DeleteIdentities(ctx context.Context, q db.Querier, appID, userID uuid.UUID) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		DELETE FROM user_identities WHERE app_id = ? AND user_id = ?`), appID, userID)
	return db.MapError(err)
}

// GetIdentityBySubject loads an identity by provider subject.
func (s *Store) GetIdentityBySubject(ctx context.Context, q db.Querier, appID uuid.UUID, provider, subject string) (Identity, error) {
	var row Identity
	err := s.q(q).GetContext(ctx, &row, s.db.Rebind(`
		SELECT id, app_id, user_id, provider, provider_subject, email, email_verified,
			provider_refresh_enc, created_at, last_used_at
		FROM user_identities WHERE app_id = ? AND provider = ? AND provider_subject = ?`),
		appID, provider, subject)
	if err != nil {
		return Identity{}, db.MapError(err)
	}
	return row, nil
}

// InsertIdentity creates a linked identity.
func (s *Store) InsertIdentity(ctx context.Context, q db.Querier, id Identity) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO user_identities (
			id, app_id, user_id, provider, provider_subject, email, email_verified,
			provider_refresh_enc, created_at, last_used_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		id.ID, id.AppID, id.UserID, id.Provider, id.ProviderSubject, id.Email, s.db.BoolArg(id.EmailVerified),
		id.ProviderRefreshEnc, id.CreatedAt, id.LastUsedAt)
	return db.MapError(err)
}

// TouchIdentity updates last_used_at and optional refresh blob.
func (s *Store) TouchIdentity(ctx context.Context, q db.Querier, appID, id uuid.UUID, refreshEnc []byte, now db.Time) error {
	if refreshEnc != nil {
		_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
			UPDATE user_identities SET last_used_at = ?, provider_refresh_enc = ?
			WHERE app_id = ? AND id = ?`), now, refreshEnc, appID, id)
		return db.MapError(err)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE user_identities SET last_used_at = ? WHERE app_id = ? AND id = ?`),
		now, appID, id)
	return db.MapError(err)
}

// TakeoverUnverifiedPassword clears password and marks email verified (linking defense).
func (s *Store) TakeoverUnverifiedPassword(ctx context.Context, q db.Querier, appID, userID uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET password_hash = NULL, email_verified_at = COALESCE(email_verified_at, ?), updated_at = ?
		WHERE app_id = ? AND id = ?`), now, now, appID, userID)
	return db.MapError(err)
}

// SetDisplayNameIfEmpty sets display_name only when currently empty.
func (s *Store) SetDisplayNameIfEmpty(ctx context.Context, q db.Querier, appID, userID uuid.UUID, name string, now db.Time) error {
	if name == "" {
		return nil
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET display_name = ?, updated_at = ?
		WHERE app_id = ? AND id = ? AND display_name = ''`), name, now, appID, userID)
	return db.MapError(err)
}

const selectOAuthFlow = `SELECT id, app_id, provider, connection_id, state_hash, redirect_uri,
	client_code_challenge, provider_verifier_enc, nonce, attribution, referral_code,
	login_code_hash, user_id, status, expires_at, created_at FROM oauth_flows`

// InsertOAuthFlow stores a new OAuth flow.
func (s *Store) InsertOAuthFlow(ctx context.Context, q db.Querier, f OAuthFlow) error {
	attr := f.Attribution
	if len(attr) == 0 {
		attr = []byte(`{}`)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauth_flows (
			id, app_id, provider, connection_id, state_hash, redirect_uri,
			client_code_challenge, provider_verifier_enc, nonce, attribution, referral_code,
			login_code_hash, user_id, status, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		f.ID, f.AppID, f.Provider, f.ConnectionID, f.StateHash, f.RedirectURI,
		f.ClientCodeChallenge, f.ProviderVerifierEnc, f.Nonce, attr, f.ReferralCode,
		f.LoginCodeHash, f.UserID, f.Status, f.ExpiresAt, f.CreatedAt)
	return db.MapError(err)
}

// GetOAuthFlowByStateHash loads a flow by state hash.
func (s *Store) GetOAuthFlowByStateHash(ctx context.Context, q db.Querier, hash []byte) (OAuthFlow, error) {
	var f OAuthFlow
	err := s.q(q).GetContext(ctx, &f, s.db.Rebind(selectOAuthFlow+` WHERE state_hash = ?`), hash)
	if err != nil {
		return OAuthFlow{}, db.MapError(err)
	}
	return f, nil
}

// GetOAuthFlowByLoginCodeHash loads an authenticated flow by login code hash.
func (s *Store) GetOAuthFlowByLoginCodeHash(ctx context.Context, q db.Querier, hash []byte) (OAuthFlow, error) {
	var f OAuthFlow
	err := s.q(q).GetContext(ctx, &f, s.db.Rebind(selectOAuthFlow+` WHERE login_code_hash = ?`), hash)
	if err != nil {
		return OAuthFlow{}, db.MapError(err)
	}
	return f, nil
}

// AuthenticateOAuthFlow marks flow authenticated with login code + user.
func (s *Store) AuthenticateOAuthFlow(ctx context.Context, q db.Querier, id uuid.UUID, userID uuid.UUID, loginHash []byte, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE oauth_flows SET status = ?, user_id = ?, login_code_hash = ?, expires_at = ?
		WHERE id = ? AND status = ?`),
		FlowAuthenticated, userID, loginHash, db.Time(now.Time().Add(2*time.Minute)), id, FlowStarted)
	return db.MapError(err)
}

// MarkOAuthFlowExchanged marks a flow exchanged (single use).
func (s *Store) MarkOAuthFlowExchanged(ctx context.Context, q db.Querier, id uuid.UUID) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE oauth_flows SET status = ? WHERE id = ? AND status = ?`),
		FlowExchanged, id, FlowAuthenticated)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// FailOAuthFlow marks a flow failed.
func (s *Store) FailOAuthFlow(ctx context.Context, q db.Querier, id uuid.UUID) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE oauth_flows SET status = ? WHERE id = ?`), FlowFailed, id)
	return db.MapError(err)
}

// InsertRefresh stores a refresh token hash.
func (s *Store) InsertRefresh(ctx context.Context, q db.Querier, rt RefreshToken) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO refresh_tokens (
			id, app_id, user_id, family_id, token_hash, auth_method, expires_at,
			revoked_at, replaced_by, user_agent, ip, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		rt.ID, rt.AppID, rt.UserID, rt.FamilyID, rt.TokenHash, rt.AuthMethod, rt.ExpiresAt,
		rt.RevokedAt, rt.ReplacedBy, rt.UserAgent, rt.IP, rt.CreatedAt)
	return db.MapError(err)
}

// GetRefreshByHash loads a refresh token by hash.
func (s *Store) GetRefreshByHash(ctx context.Context, q db.Querier, hash []byte) (RefreshToken, error) {
	var rt RefreshToken
	err := s.q(q).GetContext(ctx, &rt, s.db.Rebind(`
		SELECT id, app_id, user_id, family_id, token_hash, auth_method, expires_at,
			revoked_at, replaced_by, user_agent, ip, created_at
		FROM refresh_tokens WHERE token_hash = ?`), hash)
	if err != nil {
		return RefreshToken{}, db.MapError(err)
	}
	return rt, nil
}

// MarkRefreshReplaced revokes a token and points to its replacement.
func (s *Store) MarkRefreshReplaced(ctx context.Context, q db.Querier, appID, id, replacedBy uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE refresh_tokens SET revoked_at = ?, replaced_by = ? WHERE app_id = ? AND id = ?`),
		now, replacedBy, appID, id)
	return db.MapError(err)
}

// RevokeRefreshFamily revokes all tokens in a family.
func (s *Store) RevokeRefreshFamily(ctx context.Context, q db.Querier, appID, familyID uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?)
		WHERE app_id = ? AND family_id = ? AND revoked_at IS NULL`),
		now, appID, familyID)
	return db.MapError(err)
}

// RevokeAllRefreshForUser revokes every refresh token for a user.
func (s *Store) RevokeAllRefreshForUser(ctx context.Context, q db.Querier, appID, userID uuid.UUID, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE refresh_tokens SET revoked_at = COALESCE(revoked_at, ?)
		WHERE app_id = ? AND user_id = ? AND revoked_at IS NULL`),
		now, appID, userID)
	return db.MapError(err)
}

// InsertEmailToken stores a hashed email token.
func (s *Store) InsertEmailToken(ctx context.Context, q db.Querier, et EmailToken) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO email_tokens (
			id, app_id, user_id, purpose, email, token_hash, expires_at, consumed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		et.ID, et.AppID, et.UserID, et.Purpose, et.Email, et.TokenHash, et.ExpiresAt, et.ConsumedAt, et.CreatedAt)
	return db.MapError(err)
}

// GetEmailTokenByHash loads an email token.
func (s *Store) GetEmailTokenByHash(ctx context.Context, q db.Querier, hash []byte) (EmailToken, error) {
	var et EmailToken
	err := s.q(q).GetContext(ctx, &et, s.db.Rebind(`
		SELECT id, app_id, user_id, purpose, email, token_hash, expires_at, consumed_at, created_at
		FROM email_tokens WHERE token_hash = ?`), hash)
	if err != nil {
		return EmailToken{}, db.MapError(err)
	}
	return et, nil
}

// ConsumeEmailToken marks a token consumed if still valid.
func (s *Store) ConsumeEmailToken(ctx context.Context, q db.Querier, appID, id uuid.UUID, now db.Time) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE email_tokens SET consumed_at = ?
		WHERE app_id = ? AND id = ? AND consumed_at IS NULL AND expires_at > ?`),
		now, appID, id, now)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// ConsumeEmailTokensForUserPurpose consumes all tokens of a purpose for a user.
func (s *Store) ConsumeEmailTokensForUserPurpose(ctx context.Context, q db.Querier, appID, userID uuid.UUID, purpose string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE email_tokens SET consumed_at = COALESCE(consumed_at, ?)
		WHERE app_id = ? AND user_id = ? AND purpose = ? AND consumed_at IS NULL`),
		now, appID, userID, purpose)
	return db.MapError(err)
}

// ResetPasswordAndVerify sets password, marks email verified, clears reset tokens.
func (s *Store) ResetPasswordAndVerify(ctx context.Context, q db.Querier, appID, userID uuid.UUID, hash string, now db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE users SET password_hash = ?, email_verified_at = COALESCE(email_verified_at, ?), updated_at = ?
		WHERE app_id = ? AND id = ?`), hash, now, now, appID, userID)
	return db.MapError(err)
}

// IsNotFound reports db.ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, db.ErrNotFound) }
