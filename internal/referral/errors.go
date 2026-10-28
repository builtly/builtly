package referral

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrDisabled       = apperr.New(http.StatusUnprocessableEntity, "referral_disabled", "referral program is disabled")
	ErrCodeInvalid    = apperr.New(http.StatusUnprocessableEntity, "referral_code_invalid", "referral code is invalid")
	ErrSelf           = apperr.New(http.StatusUnprocessableEntity, "referral_self", "cannot use your own referral code")
	ErrAlreadyApplied = apperr.New(http.StatusConflict, "referral_already_applied", "referral already applied")
	ErrWindowClosed   = apperr.New(http.StatusUnprocessableEntity, "referral_window_closed", "referral apply window is closed")
	ErrNotFound       = apperr.New(http.StatusNotFound, "referral_not_found", "referral not found")
	ErrValidation     = apperr.New(http.StatusBadRequest, "validation_failed", "invalid request")
)
