package storage

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrFileNotFound             = apperr.New(http.StatusNotFound, "file_not_found", "file not found")
	ErrFileTooLarge             = apperr.New(http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds max size")
	ErrContentTypeNotAllowed    = apperr.New(http.StatusUnprocessableEntity, "content_type_not_allowed", "content type not allowed")
	ErrQuotaExceeded            = apperr.New(http.StatusUnprocessableEntity, "quota_exceeded", "storage quota exceeded")
	ErrUploadMismatch           = apperr.New(http.StatusUnprocessableEntity, "upload_mismatch", "uploaded object does not match declaration")
	ErrIntegrationNotConfigured = apperr.New(http.StatusUnprocessableEntity, "integration_not_configured", "storage integration is not configured")
	ErrValidationFailed         = apperr.New(http.StatusBadRequest, "validation_failed", "invalid request")
	ErrUpstream                 = apperr.UpstreamError
)
