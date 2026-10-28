package outbox

import (
	"errors"
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

// ErrNotConfigured is returned by subscribers when the integration is missing;
// the dispatcher marks the row skipped.
var ErrNotConfigured = errors.New("outbox: not configured")

// ErrRetryable marks a transient delivery failure.
type ErrRetryable struct{ Err error }

func (e ErrRetryable) Error() string { return e.Err.Error() }
func (e ErrRetryable) Unwrap() error { return e.Err }

// ErrPermanent marks a non-retryable delivery failure.
type ErrPermanent struct{ Err error }

func (e ErrPermanent) Error() string { return e.Err.Error() }
func (e ErrPermanent) Unwrap() error { return e.Err }

var (
	ErrNotFound = apperr.New(http.StatusNotFound, "outbox_not_found", "outbox row not found")
)
