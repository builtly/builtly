package tenant

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrAppNotFound      = apperr.AppNotFound
	ErrAppSuspended     = apperr.AppSuspended
	ErrOriginNotAllowed = apperr.OriginNotAllowed
	ErrMissingAppID     = apperr.MissingAppID
	ErrInvalidAppID     = apperr.InvalidAppID
	ErrSlugTaken        = apperr.New(http.StatusConflict, "slug_taken", "app slug already exists")
	ErrInvalidSlug      = apperr.New(http.StatusBadRequest, "invalid_slug", "slug must match [a-z0-9-]{3,40}")
	ErrInvalidSettings  = apperr.New(http.StatusBadRequest, "invalid_settings", "invalid app settings")
	ErrAPIKeyNotFound   = apperr.New(http.StatusNotFound, "api_key_not_found", "api key not found")
	ErrAPIKeyRevoked    = apperr.New(http.StatusUnauthorized, "api_key_revoked", "api key revoked")
)
