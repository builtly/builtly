package auth

import (
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// User is an app-scoped account.
type User struct {
	ID              uuid.UUID   `db:"id" json:"id"`
	AppID           uuid.UUID   `db:"app_id" json:"app_id"`
	Email           *string     `db:"email" json:"email"`
	EmailVerifiedAt db.NullTime `db:"email_verified_at" json:"email_verified_at,omitempty"`
	PasswordHash    *string     `db:"password_hash" json:"-"`
	DisplayName     string      `db:"display_name" json:"display_name"`
	AvatarURL       string      `db:"avatar_url" json:"avatar_url"`
	Locale          string      `db:"locale" json:"locale"`
	LastIP          string      `db:"last_ip" json:"last_ip"`
	Status          string      `db:"status" json:"status"`
	Attribution     []byte      `db:"attribution" json:"-"`
	ReferralCode    *string     `db:"referral_code" json:"referral_code,omitempty"`
	LastLoginAt     db.NullTime `db:"last_login_at" json:"last_login_at,omitempty"`
	DeletedAt       db.NullTime `db:"deleted_at" json:"-"`
	CreatedAt       db.Time     `db:"created_at" json:"created_at"`
	UpdatedAt       db.Time     `db:"updated_at" json:"updated_at"`
}

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"

	MethodPassword = "pwd"
	MethodGoogle   = "google"
	MethodGitHub   = "github"
	MethodApple    = "apple"

	PurposeVerifyEmail   = "verify_email"
	PurposeResetPassword = "reset_password"

	FlowStarted       = "started"
	FlowAuthenticated = "authenticated"
	FlowExchanged     = "exchanged"
	FlowFailed        = "failed"
)

// OAuthFlow is server-side OAuth state (no cookies).
type OAuthFlow struct {
	ID                  uuid.UUID  `db:"id"`
	AppID               uuid.UUID  `db:"app_id"`
	Provider            string     `db:"provider"`
	ConnectionID        string     `db:"connection_id"`
	StateHash           []byte     `db:"state_hash"`
	RedirectURI         string     `db:"redirect_uri"`
	ClientCodeChallenge string     `db:"client_code_challenge"`
	ProviderVerifierEnc []byte     `db:"provider_verifier_enc"`
	Nonce               string     `db:"nonce"`
	Attribution         []byte     `db:"attribution"`
	ReferralCode        *string    `db:"referral_code"`
	LoginCodeHash       []byte     `db:"login_code_hash"`
	UserID              *uuid.UUID `db:"user_id"`
	Status              string     `db:"status"`
	ExpiresAt           db.Time    `db:"expires_at"`
	CreatedAt           db.Time    `db:"created_at"`
}

// Identity is a linked social login.
type Identity struct {
	ID                 uuid.UUID `db:"id" json:"id"`
	AppID              uuid.UUID `db:"app_id" json:"app_id"`
	UserID             uuid.UUID `db:"user_id" json:"user_id"`
	Provider           string    `db:"provider" json:"provider"`
	ProviderSubject    string    `db:"provider_subject" json:"provider_subject"`
	Email              *string   `db:"email" json:"email,omitempty"`
	EmailVerified      bool      `db:"email_verified" json:"email_verified"`
	ProviderRefreshEnc []byte    `db:"provider_refresh_enc" json:"-"`
	CreatedAt          db.Time   `db:"created_at" json:"created_at"`
	LastUsedAt         db.Time   `db:"last_used_at" json:"last_used_at"`
}

// RefreshToken is a persisted refresh-token row (hash only).
type RefreshToken struct {
	ID         uuid.UUID   `db:"id"`
	AppID      uuid.UUID   `db:"app_id"`
	UserID     uuid.UUID   `db:"user_id"`
	FamilyID   uuid.UUID   `db:"family_id"`
	TokenHash  []byte      `db:"token_hash"`
	AuthMethod string      `db:"auth_method"`
	ExpiresAt  db.Time     `db:"expires_at"`
	RevokedAt  db.NullTime `db:"revoked_at"`
	ReplacedBy *uuid.UUID  `db:"replaced_by"`
	UserAgent  string      `db:"user_agent"`
	IP         string      `db:"ip"`
	CreatedAt  db.Time     `db:"created_at"`
}

// EmailToken is a verify/reset token row (hash only).
type EmailToken struct {
	ID         uuid.UUID   `db:"id"`
	AppID      uuid.UUID   `db:"app_id"`
	UserID     uuid.UUID   `db:"user_id"`
	Purpose    string      `db:"purpose"`
	Email      string      `db:"email"`
	TokenHash  []byte      `db:"token_hash"`
	ExpiresAt  db.Time     `db:"expires_at"`
	ConsumedAt db.NullTime `db:"consumed_at"`
	CreatedAt  db.Time     `db:"created_at"`
}

// AccessInfo is the verified access JWT payload.
type AccessInfo struct {
	UserID        uuid.UUID
	IssuedAt      time.Time
	EmailVerified bool
	AMR           []string
}

// TokenResponse is the public session payload.
type TokenResponse struct {
	User      PublicUser            `json:"user"`
	Tokens    Tokens                `json:"tokens"`
	IsNewUser bool                  `json:"is_new_user"`
	Referral  *ReferralSignupResult `json:"referral,omitempty"`
}

// ReferralSignupResult reports soft referral apply at sign-up.
type ReferralSignupResult struct {
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

// Tokens holds issued credentials.
type Tokens struct {
	TokenType        string `json:"token_type"`
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

// PublicUser is the user object returned by auth endpoints.
type PublicUser struct {
	ID            uuid.UUID `json:"id"`
	Email         *string   `json:"email"`
	EmailVerified bool      `json:"email_verified"`
	DisplayName   string    `json:"display_name"`
	AvatarURL     string    `json:"avatar_url"`
	CreatedAt     db.Time   `json:"created_at"`
}

// MeResponse is GET /user/me.
type MeResponse struct {
	ID            uuid.UUID    `json:"id"`
	Email         *string      `json:"email"`
	EmailVerified bool         `json:"email_verified"`
	DisplayName   string       `json:"display_name"`
	AvatarURL     string       `json:"avatar_url"`
	Locale        string       `json:"locale"`
	LastIP        string       `json:"last_ip"`
	UTMSource     string       `json:"utm_source"`
	Identities    []MeIdentity `json:"identities"`
	HasPassword   bool         `json:"has_password"`
	CreatedAt     db.Time      `json:"created_at"`
}

// MeIdentity is a linked provider summary.
type MeIdentity struct {
	Provider string  `json:"provider"`
	Email    *string `json:"email"`
}

func toPublicUser(u User) PublicUser {
	return PublicUser{
		ID:            u.ID,
		Email:         u.Email,
		EmailVerified: u.EmailVerifiedAt.Valid,
		DisplayName:   u.DisplayName,
		AvatarURL:     u.AvatarURL,
		CreatedAt:     u.CreatedAt,
	}
}

// PublicConfig is the client-facing app/auth configuration returned by GET /api/v1/auth/config.
type PublicConfig struct {
	AppID              uuid.UUID            `json:"app_id"`
	Slug               string               `json:"slug"`
	Name               string               `json:"name"`
	Auth               PublicAuthConfig     `json:"auth"`
	SignupBonusCredits int64                `json:"signup_bonus_credits"`
	Storage            PublicStorageConfig  `json:"storage"`
	Referral           PublicReferralConfig `json:"referral"`
}

type PublicAuthConfig struct {
	PasswordEnabled          bool                         `json:"password_enabled"`
	RequireEmailVerification bool                         `json:"require_email_verification"`
	Providers                map[string]PublicOAuthConfig `json:"providers"`
}

type PublicOAuthConfig struct {
	Enabled  bool   `json:"enabled"`
	ClientID string `json:"client_id,omitempty"`
}

type PublicStorageConfig struct {
	MaxFileBytes        int64    `json:"max_file_bytes"`
	AllowedContentTypes []string `json:"allowed_content_types"`
}

type PublicReferralConfig struct {
	Enabled             bool  `json:"enabled"`
	RefereeBonusCredits int64 `json:"referee_bonus_credits"`
}
