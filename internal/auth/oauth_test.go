package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/jwks"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

func pkcePair() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func newOAuthHarness(t *testing.T, provReg *provider.Registry, defaults config.DefaultConnections) (*server.Server, *tenant.Service, *auth.Service, *db.DB) {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	jwtKey := make([]byte, 32)
	_, _ = rand.Read(jwtKey)
	cfg := &config.Config{
		Env: "development", PublicURL: "https://api.example.com",
		AdminEnabled: true, AdminToken: adminToken,
		JWTAccessTTL: time.Minute, JWTRefreshTTL: time.Hour,
		JWTSigningKeys:     []config.KeyMaterial{{ID: "dev", Key: jwtKey}},
		DefaultConnections: defaults,
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	tMW := tenant.NewMiddleware(tSvc, 600)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	userStore := auth.NewStore(database)
	creditSvc := credit.NewService(database, credit.NewStore(database))
	tokens := auth.NewTokenService(cfg.JWTSigningKeys, cfg.JWTAccessTTL)
	outboxStore := outbox.NewStore(database)
	oreg := outbox.NewRegistry()
	oreg.DefaultSubscriptions()
	pub := outbox.NewPublisher(outboxStore, box, oreg)
	authSvc := auth.NewService(database, userStore, tokens, creditSvc, pub, cfg.JWTRefreshTTL)
	authSvc.SetOAuth(auth.OAuthDeps{
		Providers: provReg, Resolver: resolver, Box: box, PublicURL: cfg.PublicURL, Tenants: tSvc,
	})
	authH := auth.NewHandler(authSvc, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, admin.NewAuditStore(database), tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, nil, nil, nil)
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: auth.NewMiddleware(tokens, tSvc), Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc), AI: ai.NewHandler(creditSvc, userStore), Auth: authH,
	})
	return srv, tSvc, authSvc, database
}

func TestOAuthRedirectURIRejected(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	srv, tSvc, _, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "c", ClientSecret: "s"},
	})
	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		AuthRedirectURLs: []string{"https://acme.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, challenge := pkcePair()
	req := httptest.NewRequest("GET", "/api/v1/auth/oauth/github?app_id="+app.ID.String()+
		"&redirect_uri="+url.QueryEscape("https://evil.com/cb")+
		"&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestOAuthGitHubFlowAndPKCE(t *testing.T) {
	var gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ghtok","token_type":"bearer"}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"login":"octo","name":"Octo","avatar_url":""}`))
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"octo@ex.com","primary":true,"verified":true}]`))
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	gh := &provider.GitHubTestProvider{
		HTTP: upstream.Client(), AuthURL: upstream.URL + "/authorize",
		TokenURL: upstream.URL + "/login/oauth/access_token", APIBase: upstream.URL,
	}
	srv, tSvc, _, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "cid", ClientSecret: "sec"},
	})
	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		AuthRedirectURLs: []string{"https://acme.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
		Email:            tenant.EmailSettings{FromAddress: "n@acme.com", ActionBaseURL: "https://acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientVerifier, challenge := pkcePair()
	req := httptest.NewRequest("GET", "/api/v1/auth/oauth/github?app_id="+app.ID.String()+
		"&redirect_uri="+url.QueryEscape("https://acme.com/cb")+
		"&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	state := mustQuery(t, rr.Header().Get("Location"), "state")

	req = httptest.NewRequest("GET", "/api/v1/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", rr.Code, rr.Body.String())
	}
	if gotVerifier == "" {
		t.Fatal("provider PKCE verifier not posted")
	}
	loginCode := mustQuery(t, rr.Header().Get("Location"), "code")
	if !strings.HasPrefix(loginCode, "lc_") {
		t.Fatalf("login code %q", loginCode)
	}

	bad := `{"code":"` + loginCode + `","code_verifier":"wrong"}`
	req = httptest.NewRequest("POST", "/api/v1/auth/oauth/exchange", strings.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("bad pkce want 401, got %d", rr.Code)
	}

	good, _ := json.Marshal(map[string]string{"code": loginCode, "code_verifier": clientVerifier})
	req = httptest.NewRequest("POST", "/api/v1/auth/oauth/exchange", strings.NewReader(string(good)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("exchange: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("POST", "/api/v1/auth/oauth/exchange", strings.NewReader(string(good)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("reuse want 401, got %d", rr.Code)
	}
}

func TestOAuthLinkingTakeover(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := jwks.New(nil)
	cache.SetKeysForTest("https://www.googleapis.com/oauth2/v3/certs", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	provReg := provider.NewRegistry(nil, cache)

	_, tSvc, authSvc, _ := newOAuthHarness(t, provReg, config.DefaultConnections{
		GoogleOAuth: &config.GoogleOAuthDefault{ClientID: "web", ClientSecret: "s", NativeClientIDs: []string{"native"}},
	})
	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		Auth:  tenant.AuthSettings{PasswordEnabled: true, Providers: []string{"google"}},
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: "same@ex.com", Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	refresh := reg.Tokens.RefreshToken

	idTok := signGoogle(t, rsaKey, "web", "sub-1", "same@ex.com", true)
	resp, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.Refresh(context.Background(), app, refresh, "", ""); err == nil {
		t.Fatal("expected refresh revoked after takeover")
	}
	me, err := authSvc.Me(context.Background(), app.ID, resp.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if me.HasPassword || !me.EmailVerified {
		t.Fatalf("takeover failed: %#v", me)
	}
}

func TestOAuthStateExpiry(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	_, tSvc, authSvc, database := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "c", ClientSecret: "s"},
	})
	app, _ := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		AuthRedirectURLs: []string{"https://acme.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
	})
	_, challenge := pkcePair()
	authURL, err := authSvc.StartOAuth(context.Background(), "github", app.ID, "https://acme.com/cb", challenge, "S256", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	state := mustQuery(t, authURL, "state")
	_, err = database.ExecContext(context.Background(), database.Rebind(`UPDATE oauth_flows SET expires_at = ?`),
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	redir, err := authSvc.HandleOAuthCallback(context.Background(), "github", "code", state, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(redir, "error=access_denied") {
		t.Fatalf("want access_denied, got %s", redir)
	}
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := u.Query().Get(key)
	if v == "" {
		t.Fatalf("missing %s in %s", key, raw)
	}
	return v
}

func signGoogle(t *testing.T, key *rsa.PrivateKey, aud, sub, email string, verified bool) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://accounts.google.com", "sub": sub, "aud": aud,
		"email": email, "email_verified": verified,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "test"
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
