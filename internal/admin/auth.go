package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/ratelimit"
)

// Auth middleware protects /api/v1/admin/* with the master admin token.
type Auth struct {
	tokenHash      [32]byte
	allowed        []*net.IPNet
	trustedProxies []*net.IPNet
	failLimit      *ratelimit.Limiter
}

// NewAuth builds Auth from process config.
func NewAuth(cfg *config.Config) *Auth {
	sum := sha256.Sum256([]byte(cfg.AdminToken))
	return &Auth{
		tokenHash:      sum,
		allowed:        cfg.AdminAllowed,
		trustedProxies: cfg.TrustedProxies,
		failLimit:      ratelimit.New(5, 5),
	}
}

// Middleware enforces CIDR allow-list (404 if denied) and Bearer token (401 if bad).
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r, a.trustedProxies)
		if !a.ipAllowed(ip) {
			httpx.WriteJSON(w, http.StatusNotFound, map[string]any{
				"error": map[string]string{"code": "not_found", "message": "not found"},
			})
			return
		}
		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			a.fail(w, r, ip)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
		got := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(got[:], a.tokenHash[:]) != 1 {
			a.fail(w, r, ip)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) fail(w http.ResponseWriter, r *http.Request, ip string) {
	if !a.failLimit.Allow(ip) {
		httpx.WriteError(w, r, apperr.TooManyRequests)
		return
	}
	logx.From(r.Context()).Warn("admin auth failure", "remote_ip", ip)
	httpx.WriteError(w, r, apperr.Unauthorized)
}

func (a *Auth) ipAllowed(ipStr string) bool {
	if len(a.allowed) == 0 {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range a.allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
