package catalog

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrProductNotFound     = apperr.New(http.StatusNotFound, "product_not_found", "product not found")
	ErrProductNotAvailable = apperr.New(http.StatusUnprocessableEntity, "product_not_available", "product is not available")
	ErrStripePriceInvalid  = apperr.New(http.StatusUnprocessableEntity, "stripe_price_invalid", "stripe price is invalid for this connection")
	ErrValidationFailed    = apperr.New(http.StatusBadRequest, "validation_failed", "invalid product")
	ErrSKUTaken            = apperr.New(http.StatusConflict, "sku_taken", "sku already exists")
)
