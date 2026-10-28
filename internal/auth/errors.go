package auth

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrUserNotFound             = apperr.New(http.StatusNotFound, "user_not_found", "user not found")
	ErrEmailTaken               = apperr.New(http.StatusConflict, "email_taken", "email already registered")
	ErrInvalidCredentials       = apperr.New(http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
	ErrAccountDisabled          = apperr.New(http.StatusForbidden, "account_disabled", "account is disabled")
	ErrEmailNotVerified         = apperr.New(http.StatusForbidden, "email_not_verified", "email is not verified")
	ErrProviderDisabled         = apperr.New(http.StatusForbidden, "provider_disabled", "password authentication is disabled")
	ErrInvalidToken             = apperr.New(http.StatusUnauthorized, "invalid_token", "invalid or expired token")
	ErrWeakPassword             = apperr.New(http.StatusBadRequest, "weak_password", "password must be 8–128 characters")
	ErrInvalidEmail             = apperr.New(http.StatusBadRequest, "invalid_email", "invalid email address")
	ErrReauthRequired           = apperr.New(http.StatusForbidden, "reauth_required", "password confirmation required")
	ErrRateLimited              = apperr.TooManyRequests
	ErrInvalidOAuthRequest      = apperr.New(http.StatusBadRequest, "invalid_oauth_request", "invalid oauth request")
	ErrInvalidRedirectURI       = apperr.New(http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri is not allow-listed")
	ErrEmailRequired            = apperr.New(http.StatusUnprocessableEntity, "email_required", "a verified email is required")
	ErrIntegrationNotConfigured = apperr.New(http.StatusUnprocessableEntity, "integration_not_configured", "oauth provider is not configured")
)
