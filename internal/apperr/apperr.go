// Package apperr defines the structured application error returned by services
// and rendered by httpx.WriteError.
package apperr

import (
	"fmt"
	"net/http"
)

// Error is a domain error with a stable machine-readable Code and HTTP Status.
type Error struct {
	Code    string         `json:"code"`
	Status  int            `json:"-"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New constructs an Error.
func New(status int, code, message string) *Error {
	return &Error{Code: code, Status: status, Message: message}
}

// WithDetails returns a shallow copy with Details set.
func (e *Error) WithDetails(d map[string]any) *Error {
	if e == nil {
		return nil
	}
	cp := *e
	cp.Details = d
	return &cp
}

// Common codes used across modules.
var (
	Internal         = New(http.StatusInternalServerError, "internal", "an unexpected error occurred")
	NotFound         = New(http.StatusNotFound, "not_found", "resource not found")
	Unauthorized     = New(http.StatusUnauthorized, "unauthorized", "authentication required")
	Forbidden        = New(http.StatusForbidden, "forbidden", "forbidden")
	BadRequest       = New(http.StatusBadRequest, "bad_request", "invalid request")
	Conflict         = New(http.StatusConflict, "conflict", "conflict")
	TooManyRequests  = New(http.StatusTooManyRequests, "rate_limited", "too many requests")
	UpstreamError    = New(http.StatusBadGateway, "upstream_error", "upstream service failed")
	MissingAppID     = New(http.StatusBadRequest, "missing_app_id", "X-App-ID header is required")
	InvalidAppID     = New(http.StatusBadRequest, "invalid_app_id", "X-App-ID must be a UUID")
	AppNotFound      = New(http.StatusNotFound, "app_not_found", "app not found")
	AppSuspended     = New(http.StatusForbidden, "app_suspended", "app is suspended")
	OriginNotAllowed = New(http.StatusForbidden, "origin_not_allowed", "origin is not allowed")
	PaymentRequired  = New(http.StatusPaymentRequired, "insufficient_credits", "insufficient credits")
)
