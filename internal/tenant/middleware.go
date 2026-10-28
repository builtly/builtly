package tenant

import (
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/httpx"
)

// Middleware resolves X-App-ID into context and enforces origin allow-lists.
type Middleware struct {
	svc *Service

	corsMu     sync.RWMutex
	corsSet    map[string]struct{}
	corsAt     time.Time
	corsTTL    time.Duration
	corsMaxAge int
}

// NewMiddleware constructs tenant HTTP middleware.
func NewMiddleware(svc *Service, corsMaxAge int) *Middleware {
	if corsMaxAge <= 0 {
		corsMaxAge = 600
	}
	return &Middleware{svc: svc, corsTTL: 30 * time.Second, corsMaxAge: corsMaxAge}
}

// Tenant is the per-request tenant resolver for /api/v1/* (non-admin).
func (m *Middleware) Tenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-App-ID")
		if raw == "" {
			httpx.WriteError(w, r, ErrMissingAppID)
			return
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, r, ErrInvalidAppID)
			return
		}
		app, err := m.svc.Get(r.Context(), id)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if app.Status == StatusSuspended {
			httpx.WriteError(w, r, ErrAppSuspended)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !app.Settings.AllowsOrigin(origin) {
				httpx.WriteError(w, r, ErrOriginNotAllowed)
				return
			}
		}
		ctx := WithApp(r.Context(), app)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CORS handles browser preflight using the union of active apps' origins.
func (m *Middleware) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && m.originAllowed(r, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-App-ID, Idempotency-Key, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", itoa(m.corsMaxAge))
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) originAllowed(r *http.Request, origin string) bool {
	set, err := m.corsOrigins(r)
	if err != nil {
		return false
	}
	_, ok := set[origin]
	return ok
}

func (m *Middleware) corsOrigins(r *http.Request) (map[string]struct{}, error) {
	m.corsMu.RLock()
	if m.corsSet != nil && time.Since(m.corsAt) < m.corsTTL {
		defer m.corsMu.RUnlock()
		return m.corsSet, nil
	}
	m.corsMu.RUnlock()

	origins, err := m.svc.ListActiveOrigins(r.Context())
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		set[o] = struct{}{}
	}
	m.corsMu.Lock()
	m.corsSet = set
	m.corsAt = time.Now()
	m.corsMu.Unlock()
	return set, nil
}

// InvalidateCORS drops the cached origin union (call after admin writes).
func (m *Middleware) InvalidateCORS() {
	m.corsMu.Lock()
	m.corsSet = nil
	m.corsMu.Unlock()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
