// Package principal holds the request-scoped caller identity without importing auth/credit.
package principal

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type ctxKey struct{}

// Principal is the authenticated caller (user JWT and/or API key).
type Principal struct {
	UserID   uuid.UUID
	APIKeyID uuid.UUID
	IsAPIKey bool
	Actor    string // user:<id> | api_key:<id>
	IssuedAt time.Time
}

// FromContext returns the principal attached by auth middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// With attaches a principal to ctx.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}
