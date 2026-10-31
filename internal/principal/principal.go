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
	Scopes   []string
	Actor    string // user:<id> | api_key:<id>
	IssuedAt time.Time
}

// HasScope reports whether the principal has the given scope (empty Scopes on APIKey defaults to credits:consume).
func (p Principal) HasScope(scope string) bool {
	if !p.IsAPIKey {
		return true
	}
	if len(p.Scopes) == 0 {
		return scope == "credits:consume"
	}
	for _, s := range p.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
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
