package email

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrTemplateNotFound    = apperr.New(http.StatusNotFound, "template_not_found", "email template not found")
	ErrTemplateKeyTaken    = apperr.New(http.StatusConflict, "template_key_taken", "template alias already exists")
	ErrTemplateInUse       = apperr.New(http.StatusConflict, "template_in_use", "template is bound to a system trigger")
	ErrTemplateUnpublished = apperr.New(http.StatusUnprocessableEntity, "template_unpublished", "template must be published")
	ErrInvalidTemplate     = apperr.New(http.StatusUnprocessableEntity, "invalid_template", "invalid email template")
	ErrInvalidTrigger      = apperr.New(http.StatusBadRequest, "invalid_trigger", "unknown email trigger")
	ErrNotCustomTemplate   = apperr.New(http.StatusUnprocessableEntity, "template_not_custom", "system templates cannot be sent from the client")
	ErrMissingIdempotency  = apperr.New(http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required")
	ErrIdempotencyConflict = apperr.New(http.StatusConflict, "idempotency_conflict", "idempotency key reused with different parameters")
	ErrNoRecipient         = apperr.New(http.StatusUnprocessableEntity, "email_required", "user has no email address")
	errUnauthorized        = apperr.Unauthorized
	errRateLimited         = apperr.TooManyRequests
)
