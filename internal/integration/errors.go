package integration

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrNotConfigured          = apperr.New(http.StatusUnprocessableEntity, "integration_not_configured", "integration not configured for this app")
	ErrInvalidProvider        = apperr.New(http.StatusBadRequest, "invalid_provider", "unknown provider")
	ErrConnectionNotFound     = apperr.New(http.StatusNotFound, "connection_not_found", "connection not found")
	ErrConnectionInUse        = apperr.New(http.StatusConflict, "connection_in_use", "connection is bound to one or more apps")
	ErrConnectionAlreadyBound = apperr.New(http.StatusConflict, "connection_already_bound", "exclusive connection already bound to another app")
	ErrConnectionDuplicate    = apperr.New(http.StatusConflict, "connection_duplicate", "a connection with the same account already exists")
	ErrProviderMismatch       = apperr.New(http.StatusBadRequest, "provider_mismatch", "connection provider does not match binding")
	ErrValidationFailed       = apperr.New(http.StatusUnprocessableEntity, "validation_failed", "connection validation failed")
	ErrInvalidSecrets         = apperr.New(http.StatusBadRequest, "invalid_secrets", "invalid connection secrets")
)
