package billing

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrPurchaseNotFound         = apperr.New(http.StatusNotFound, "purchase_not_found", "purchase not found")
	ErrValidationFailed         = apperr.New(http.StatusBadRequest, "validation_failed", "invalid request")
	ErrIntegrationNotConfigured = apperr.New(http.StatusUnprocessableEntity, "integration_not_configured", "billing integration is not configured")
	ErrUpstream                 = apperr.UpstreamError
	ErrProductNotAvailable      = apperr.New(http.StatusUnprocessableEntity, "product_not_available", "product is not available")
	ErrPurchaseAlreadyClaimed   = apperr.New(http.StatusConflict, "purchase_already_claimed", "purchase already claimed by another user")
	ErrReceiptInvalid           = apperr.New(http.StatusUnprocessableEntity, "receipt_invalid", "receipt is invalid")
)
