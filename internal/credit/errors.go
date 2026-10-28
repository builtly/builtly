package credit

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrInsufficientCredits = apperr.New(http.StatusPaymentRequired, "insufficient_credits", "insufficient credits")
	ErrIdempotencyConflict = apperr.New(http.StatusConflict, "idempotency_conflict", "idempotency key reused with different parameters")
	ErrConcurrentMod       = apperr.New(http.StatusConflict, "concurrent_modification", "concurrent modification; retry")
	ErrInvalidAmount       = apperr.New(http.StatusBadRequest, "invalid_amount", "invalid credit amount")
	ErrInvalidEntryType    = apperr.New(http.StatusBadRequest, "invalid_entry_type", "invalid ledger entry type")
	ErrReasonRequired      = apperr.New(http.StatusBadRequest, "reason_required", "reason is required")
	ErrMissingIdempotency  = apperr.New(http.StatusBadRequest, "missing_idempotency_key", "Idempotency-Key header is required")
)
