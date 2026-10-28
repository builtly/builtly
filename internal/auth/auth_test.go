package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

const adminToken = "test-admin-token-at-least-32-chars!!"

type harness struct {
	srv     *server.Server
	tSvc    *tenant.Service
	mail    *email.MemorySender
	outbox  *outbox.Store
	box     *secrets.Box
	authSvc *auth.Service
	disp    *outbox.Dispatcher
}

func setup(t *testing.T) *harness {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Env: "development", HTTPAddr: ":0", PublicURL: "https://api.example.com",
		AdminEnabled: true, AdminToken: adminToken,
		CORSMaxAge: 600, LogFormat: "text", LogLevel: "error",
		JWTSigningKeys: []config.KeyMaterial{{ID: "dev", Key: bytes.Repeat([]byte{9}, 32)}},
		JWTAccessTTL:   time.Minute,
		JWTRefreshTTL:  24 * time.Hour,
		DefaultConnections: config.DefaultConnections{
			Resend: &config.ResendDefault{APIKey: "re_test"},
		},
	}
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
	outboxStore := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.DefaultSubscriptions()
	tpl, err := email.LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	mail := &email.MemorySender{}
	sub := email.NewSubscriber(mail, tpl, resolver, tSvc)
	reg.SetHandler(outbox.SubEmail, sub.Handle)
	pub := outbox.NewPublisher(outboxStore, box, reg)
	disp := outbox.NewDispatcher(outboxStore, box, reg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	authSvc := auth.NewService(database, userStore, tokens, creditSvc, pub, cfg.JWTRefreshTTL)
	authH := auth.NewHandler(authSvc, nil)
	audit := admin.NewAuditStore(database)
	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, audit, tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, nil, nil, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, TenantStore: tStore,
		Integration: iSvc, IntegStore: iStore, Resolver: resolver,
		Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: authMW, Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc),
		AI:         ai.NewHandler(creditSvc, userStore),
		Auth:       authH,
	})
	return &harness{srv: srv, tSvc: tSvc, mail: mail, outbox: outboxStore, box: box, authSvc: authSvc, disp: disp}
}

func (h *harness) createApp(t *testing.T, slug string, requireVerify bool, bonus int64) tenant.App {
	t.Helper()
	app, err := h.tSvc.Create(context.Background(), slug, strings.ToUpper(slug), tenant.Settings{
		AllowedOrigins:     []string{"https://" + slug + ".com"},
		SignupBonusCredits: bonus,
		Auth: tenant.AuthSettings{
			PasswordEnabled: true, RequireEmailVerification: requireVerify,
		},
		Email: tenant.EmailSettings{
			FromName: slug, FromAddress: "noreply@" + slug + ".com",
			ActionBaseURL: "https://" + slug + ".com/auth",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (h *harness) do(method, path, appID, bearer string, body any) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if appID != "" {
		req.Header.Set("X-App-ID", appID)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	return rr
}

func drainOutbox(t *testing.T, h *harness, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		worked, err := h.disp.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
}

func TestRegisterLoginRefreshLogout(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "acme", false, 25)

	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "Ann@Example.com", "password": "password123", "display_name": "Ann",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	tokens := reg["tokens"].(map[string]any)
	access := tokens["access_token"].(string)
	refresh := tokens["refresh_token"].(string)
	user := reg["user"].(map[string]any)
	if user["email"].(string) != "ann@example.com" {
		t.Fatalf("email normalize: %#v", user["email"])
	}
	if !reg["is_new_user"].(bool) {
		t.Fatal("expected new user")
	}

	bal := h.do("GET", "/api/v1/user/credits", app.ID.String(), access, nil)
	if bal.Code != 200 {
		t.Fatalf("credits: %d %s", bal.Code, bal.Body.String())
	}
	var balBody map[string]any
	_ = json.Unmarshal(bal.Body.Bytes(), &balBody)
	if int64(balBody["balance"].(float64)) != 25 {
		t.Fatalf("bonus balance %#v", balBody)
	}

	drainOutbox(t, h, 5)
	if len(h.mail.Messages) != 1 {
		t.Fatalf("want verify mail, got %d", len(h.mail.Messages))
	}
	if !strings.Contains(h.mail.Messages[0].HTML, "token=") {
		t.Fatalf("mail missing token link: %s", h.mail.Messages[0].HTML)
	}

	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "ann@example.com", "password": "password123",
	})
	if rr.Code != 200 {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}

	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{
		"refresh_token": refresh,
	})
	if rr.Code != 200 {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body.String())
	}
	var ref map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &ref)
	newRefresh := ref["tokens"].(map[string]any)["refresh_token"].(string)
	if newRefresh == refresh {
		t.Fatal("refresh token should rotate")
	}

	// reuse old refresh → family revoked
	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{
		"refresh_token": refresh,
	})
	if rr.Code != 401 {
		t.Fatalf("reuse want 401, got %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{
		"refresh_token": newRefresh,
	})
	if rr.Code != 401 {
		t.Fatalf("family revoke want 401, got %d %s", rr.Code, rr.Body.String())
	}

	// login again and logout
	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "ann@example.com", "password": "password123",
	})
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	refresh = reg["tokens"].(map[string]any)["refresh_token"].(string)
	rr = h.do("POST", "/api/v1/auth/logout", app.ID.String(), "", map[string]any{"refresh_token": refresh})
	if rr.Code != 204 {
		t.Fatalf("logout: %d", rr.Code)
	}
	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{"refresh_token": refresh})
	if rr.Code != 401 {
		t.Fatalf("after logout want 401, got %d", rr.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	h := setup(t)
	a := h.createApp(t, "alpha", false, 0)
	b := h.createApp(t, "beta", false, 0)

	rr := h.do("POST", "/api/v1/auth/register", a.ID.String(), "", map[string]any{
		"email": "same@ex.com", "password": "password123",
	})
	if rr.Code != 201 {
		t.Fatal(rr.Body.String())
	}
	var regA map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &regA)
	accessA := regA["tokens"].(map[string]any)["access_token"].(string)

	rr = h.do("POST", "/api/v1/auth/register", b.ID.String(), "", map[string]any{
		"email": "same@ex.com", "password": "password123",
	})
	if rr.Code != 201 {
		t.Fatalf("same email other app: %d %s", rr.Code, rr.Body.String())
	}

	// JWT for A with X-App-ID B → 401
	rr = h.do("GET", "/api/v1/user/me", b.ID.String(), accessA, nil)
	if rr.Code != 401 {
		t.Fatalf("cross-app jwt want 401, got %d %s", rr.Code, rr.Body.String())
	}

	rr = h.do("GET", "/api/v1/user/me", a.ID.String(), accessA, nil)
	if rr.Code != 200 {
		t.Fatalf("me: %d %s", rr.Code, rr.Body.String())
	}
}

func TestResetRevokesSessions(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "acme", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "u@ex.com", "password": "password123",
	})
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	access := reg["tokens"].(map[string]any)["access_token"].(string)
	refresh := reg["tokens"].(map[string]any)["refresh_token"].(string)

	rr = h.do("POST", "/api/v1/auth/password/forgot", app.ID.String(), "", map[string]any{"email": "u@ex.com"})
	if rr.Code != 202 {
		t.Fatalf("forgot: %d", rr.Code)
	}
	drainOutbox(t, h, 10)
	var resetToken string
	for _, m := range h.mail.Messages {
		if strings.Contains(m.Subject, "Reset") || strings.Contains(m.Text, "Reset") {
			idx := strings.Index(m.Text, "token=")
			if idx >= 0 {
				rest := m.Text[idx+len("token="):]
				resetToken = strings.Fields(rest)[0]
				resetToken = strings.TrimSpace(resetToken)
			}
		}
	}
	if resetToken == "" {
		// parse from HTML href
		for _, m := range h.mail.Messages {
			if i := strings.Index(m.HTML, "token="); i >= 0 {
				rest := m.HTML[i+6:]
				end := strings.IndexAny(rest, "\"' <\n")
				if end < 0 {
					end = len(rest)
				}
				resetToken = rest[:end]
			}
		}
	}
	if resetToken == "" {
		t.Fatalf("no reset token in mails: %#v", h.mail.Messages)
	}

	rr = h.do("POST", "/api/v1/auth/password/reset", app.ID.String(), "", map[string]any{
		"token": resetToken, "password": "newpassword99",
	})
	if rr.Code != 204 {
		t.Fatalf("reset: %d %s", rr.Code, rr.Body.String())
	}

	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{"refresh_token": refresh})
	if rr.Code != 401 {
		t.Fatalf("refresh after reset want 401, got %d", rr.Code)
	}
	// old access may still work until expiry (stateless) — login with new password
	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "u@ex.com", "password": "newpassword99",
	})
	if rr.Code != 200 {
		t.Fatalf("login new pw: %d %s", rr.Code, rr.Body.String())
	}
	_ = access
}

func TestEmailVerificationGate(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "strict", true, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "v@ex.com", "password": "password123",
	})
	if rr.Code != 201 {
		t.Fatal(rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "v@ex.com", "password": "password123",
	})
	if rr.Code != 403 || !strings.Contains(rr.Body.String(), "email_not_verified") {
		t.Fatalf("want email_not_verified, got %d %s", rr.Code, rr.Body.String())
	}

	drainOutbox(t, h, 5)
	var verifyToken string
	for _, m := range h.mail.Messages {
		if i := strings.Index(m.HTML, "token="); i >= 0 {
			rest := m.HTML[i+6:]
			end := strings.IndexAny(rest, "\"' <\n")
			if end < 0 {
				end = len(rest)
			}
			verifyToken = rest[:end]
		}
	}
	if verifyToken == "" {
		t.Fatal("missing verify token")
	}
	rr = h.do("POST", "/api/v1/auth/email/verify", app.ID.String(), "", map[string]any{"token": verifyToken})
	if rr.Code != 200 {
		t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "v@ex.com", "password": "password123",
	})
	if rr.Code != 200 {
		t.Fatalf("login after verify: %d %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteMe(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "gone", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "d@ex.com", "password": "password123",
	})
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	access := reg["tokens"].(map[string]any)["access_token"].(string)

	rr = h.do("DELETE", "/api/v1/user/me", app.ID.String(), access, map[string]any{})
	if rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("GET", "/api/v1/user/me", app.ID.String(), access, nil)
	if rr.Code != 404 {
		t.Fatalf("me after delete want 404, got %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "d@ex.com", "password": "password123",
	})
	if rr.Code != 401 {
		t.Fatalf("login deleted want 401, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash := auth.VerifyPassword("password123", h)
	if !ok || rehash {
		t.Fatalf("verify ok=%v rehash=%v", ok, rehash)
	}
	ok, _ = auth.VerifyPassword("wrong", h)
	if ok {
		t.Fatal("wrong password accepted")
	}
}
