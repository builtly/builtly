package admin_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

const adminToken = "test-admin-token-at-least-32-chars!!"

func testServer(t *testing.T) (*server.Server, *config.Config, *db.DB) {
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
		AdminEnabled:    true,
		AdminToken:      adminToken,
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
	audit := admin.NewAuditStore(database)
	adminAuth := admin.NewAuth(cfg)
	outboxStore := outbox.NewStore(database)
	h := admin.NewHandler(database, tSvc, iSvc, resolver, audit, tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, nil, nil, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	slog.SetDefault(log)
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, TenantStore: tStore,
		Integration: iSvc, IntegStore: iStore, Resolver: resolver,
		Secrets: box, AdminAuth: adminAuth, Admin: h,
		AuthMW: authMW, Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc),
		AI:         ai.NewHandler(creditSvc, userStore),
	})
	return srv, cfg, database
}

func TestAdminRoutesRequireAuth(t *testing.T) {
	srv, _, _ := testServer(t)
	routes := []struct {
		method, path string
	}{
		{"GET", "/api/v1/admin/apps"},
		{"POST", "/api/v1/admin/apps"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001"},
		{"PATCH", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/config"},
		{"PUT", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/config"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/api-keys"},
		{"POST", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/api-keys"},
		{"DELETE", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/api-keys/00000000-0000-7000-8000-000000000002"},
		{"GET", "/api/v1/admin/connections"},
		{"POST", "/api/v1/admin/connections"},
		{"GET", "/api/v1/admin/connections/00000000-0000-7000-8000-000000000003"},
		{"PATCH", "/api/v1/admin/connections/00000000-0000-7000-8000-000000000003"},
		{"DELETE", "/api/v1/admin/connections/00000000-0000-7000-8000-000000000003"},
		{"GET", "/api/v1/admin/audit-log"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users/00000000-0000-7000-8000-000000000002"},
		{"PATCH", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users/00000000-0000-7000-8000-000000000002"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users/00000000-0000-7000-8000-000000000002/ledger"},
		{"POST", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users/00000000-0000-7000-8000-000000000002/credits"},
		{"POST", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/users/00000000-0000-7000-8000-000000000002/credits/clear-debt"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/purchases"},
		{"GET", "/api/v1/admin/apps/00000000-0000-7000-8000-000000000001/ledger"},
		{"GET", "/api/v1/admin/outbox"},
		{"POST", "/api/v1/admin/outbox/00000000-0000-7000-8000-000000000004/retry"},
	}
	for i, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.RemoteAddr = "203.0.113." + strconv.Itoa(i+1) + ":12345"
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), `"unauthorized"`) {
				t.Fatalf("body %s", rr.Body.String())
			}
		})
	}
}

func TestAdminAppFlowAndSecretRedaction(t *testing.T) {
	srv, _, _ := testServer(t)
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		return rr
	}

	rr := do("POST", "/api/v1/admin/apps", map[string]any{
		"slug": "acme", "name": "Acme",
		"settings": map[string]any{"allowed_origins": []string{"https://acme.com"}, "signup_bonus_credits": 10},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create app: %d %s", rr.Code, rr.Body.String())
	}
	var app map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &app)
	appID := app["id"].(string)

	secret := "re_super_secret_value_do_not_leak"
	rr = do("POST", "/api/v1/admin/connections", map[string]any{
		"provider": "resend", "name": "Mail",
		"settings": map[string]any{},
		"secrets":  map[string]any{"api_key": secret},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create conn: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("connection response leaked raw secret")
	}
	var conn map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &conn)
	connID := conn["id"].(string)

	rr = do("PUT", "/api/v1/admin/apps/"+appID+"/config", map[string]any{
		"settings": map[string]any{
			"allowed_origins":      []string{"https://acme.com"},
			"signup_bonus_credits": 10,
		},
		"integrations": map[string]any{
			"resend": map[string]any{"connection_id": connID},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("put config: %d %s", rr.Code, rr.Body.String())
	}

	rr = do("POST", "/api/v1/admin/apps/"+appID+"/api-keys", map[string]any{"name": "proxy"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("api key: %d %s", rr.Code, rr.Body.String())
	}
	var keyResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &keyResp)
	if keyResp["key"] == nil || !strings.HasPrefix(keyResp["key"].(string), "bk_") {
		t.Fatalf("expected raw key once: %v", keyResp)
	}

	rr = do("GET", "/api/v1/admin/audit-log", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("audit log leaked secret")
	}
	if strings.Contains(rr.Body.String(), keyResp["key"].(string)) {
		t.Fatal("audit log leaked api key")
	}
	if !strings.Contains(rr.Body.String(), "[redacted]") {
		t.Fatalf("expected redacted secrets in audit: %s", rr.Body.String())
	}
}
