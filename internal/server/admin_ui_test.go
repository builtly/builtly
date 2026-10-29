package server_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

const wantAdminCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

func adminUIServer(t *testing.T, adminEnabled bool) *server.Server {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Env:             "development",
		HTTPAddr:        ":0",
		PublicURL:       "https://api.example.com",
		AdminEnabled:    adminEnabled,
		AdminToken:      "test-admin-token-at-least-32-chars!!",
		CORSMaxAge:      600,
		ShutdownTimeout: 0,
		LogFormat:       "text",
		LogLevel:        "error",
		DefaultConnections: config.DefaultConnections{
			Resend: &config.ResendDefault{APIKey: "re_env_default_secret"},
		},
	}
	cfg.JWTSigningKeys = []config.KeyMaterial{{ID: "dev", Key: bytes.Repeat([]byte{9}, 32)}}
	cfg.JWTAccessTTL = time.Minute
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	tMW := tenant.NewMiddleware(tSvc, cfg.CORSMaxAge)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	userStore := auth.NewStore(database)
	creditSvc := credit.NewService(database, credit.NewStore(database))
	tokens := auth.NewTokenService(cfg.JWTSigningKeys, cfg.JWTAccessTTL)
	authMW := auth.NewMiddleware(tokens, tSvc)
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	slog.SetDefault(log)
	return server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, TenantStore: tStore,
		Integration: iSvc, IntegStore: iStore, Resolver: resolver,
		Secrets: box, AuthMW: authMW, Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc),
		AI:         ai.NewHandler(creditSvc, userStore),
	})
}

func TestAdminUIHeadersAndAssets(t *testing.T) {
	srv := adminUIServer(t, true)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /admin/: status %d body=%s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type=%q want text/html", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{"app.js", "vendor/pico.min.css", "vendor/alpine.csp.min.js"} {
		if !strings.Contains(body, want) {
			t.Fatalf("html missing %s: %s", want, body)
		}
	}
	if rr.Header().Get("Content-Security-Policy") != wantAdminCSP {
		t.Fatalf("CSP=%q", rr.Header().Get("Content-Security-Policy"))
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options=%q", rr.Header().Get("X-Frame-Options"))
	}
	if rr.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control=%q", rr.Header().Get("Cache-Control"))
	}

	for _, path := range []string{
		"/admin/app.js", "/admin/vendor/pico.min.css", "/admin/vendor/alpine.csp.min.js",
		"/admin/icon.svg", "/admin/favicon.ico", "/admin/favicon-32.png", "/admin/apple-touch-icon.png",
	} {
		rr = httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, rr.Code)
		}
		ct := rr.Header().Get("Content-Type")
		if strings.HasPrefix(ct, "text/html") || strings.HasPrefix(rr.Body.String(), "<!DOCTYPE html>") {
			t.Fatalf("GET %s: SPA fallback leaked HTML (Content-Type=%q)", path, ct)
		}
	}
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/app.js", nil))
	if !strings.Contains(rr.Body.String(), "hashchange") {
		t.Fatalf("app.js unexpected body prefix: %s", rr.Body.String()[:min(200, rr.Body.Len())])
	}
	if rr.Header().Get("Content-Security-Policy") != wantAdminCSP {
		t.Fatalf("app.js CSP=%q", rr.Header().Get("Content-Security-Policy"))
	}
	if rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("app.js X-Frame-Options=%q", rr.Header().Get("X-Frame-Options"))
	}
}

func TestAdminUIDisabled(t *testing.T) {
	srv := adminUIServer(t, false)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 when admin disabled, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminUISPAFallback(t *testing.T) {
	srv := adminUIServer(t, true)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/apps", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /admin/apps: status %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "app.js") {
		t.Fatalf("SPA fallback should return index.html, got: %s", body[:min(300, len(body))])
	}
	if rr.Header().Get("Content-Security-Policy") != wantAdminCSP {
		t.Fatalf("CSP=%q", rr.Header().Get("Content-Security-Policy"))
	}
}

func TestAdminUIRedirect(t *testing.T) {
	srv := adminUIServer(t, true)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("want 301, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/admin/" {
		t.Fatalf("Location=%q", loc)
	}
}
