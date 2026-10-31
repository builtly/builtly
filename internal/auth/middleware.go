package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// FromContext is an alias for principal.FromContext.
func FromContext(ctx context.Context) (principal.Principal, bool) {
	return principal.FromContext(ctx)
}

// Middleware authenticates user JWT or app API key.
type Middleware struct {
	tokens *TokenService
	tenant *tenant.Service
}

// NewMiddleware constructs auth middleware.
func NewMiddleware(tokens *TokenService, tSvc *tenant.Service) *Middleware {
	return &Middleware{tokens: tokens, tenant: tSvc}
}

// Authenticate requires a Bearer user JWT or bk_ API key; app must already be in context.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app, ok := tenant.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, r, apperr.Unauthorized)
			return
		}
		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			httpx.WriteError(w, r, apperr.Unauthorized)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		if strings.HasPrefix(token, "bk_") {
			k, err := m.tenant.LookupAPIKey(r.Context(), token)
			if err != nil {
				httpx.WriteError(w, r, apperr.Unauthorized)
				return
			}
			if k.AppID != app.ID {
				httpx.WriteError(w, r, apperr.Unauthorized)
				return
			}
			var scopes []string
			_ = json.Unmarshal(k.Scopes, &scopes)
			p := principal.Principal{APIKeyID: k.ID, IsAPIKey: true, Scopes: scopes, Actor: "api_key:" + k.ID.String()}
			next.ServeHTTP(w, r.WithContext(principal.With(r.Context(), p)))
			return
		}
		info, err := m.tokens.VerifyAccess(token, app.ID)
		if err != nil {
			httpx.WriteError(w, r, apperr.Unauthorized)
			return
		}
		p := principal.Principal{
			UserID: info.UserID, Actor: "user:" + info.UserID.String(),
			IssuedAt: info.IssuedAt,
		}
		next.ServeHTTP(w, r.WithContext(principal.With(r.Context(), p)))
	})
}

// RequireUser rejects API-key-only principals.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok || p.IsAPIKey || p.UserID == uuid.Nil {
			httpx.WriteError(w, r, apperr.Unauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
