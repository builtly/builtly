package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/jwks"
	"github.com/builtly/builtly/internal/tenant"
)

func TestPatchMeAndRequestVerify(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "profile", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "p@ex.com", "password": "password123", "display_name": "Old",
		"locale": "en-US", "attribution": map[string]any{"utm_source": "newsletter"},
	})
	if rr.Code != 201 {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	access := reg["tokens"].(map[string]any)["access_token"].(string)

	rr = h.do("PATCH", "/api/v1/user/me", app.ID.String(), access, map[string]any{
		"display_name": "New Name", "avatar_url": "https://cdn.example/a.png", "locale": "zh-CN",
	})
	if rr.Code != 200 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	var me map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
	if me["display_name"] != "New Name" || me["locale"] != "zh-CN" || me["avatar_url"] != "https://cdn.example/a.png" {
		t.Fatalf("me=%v", me)
	}
	if me["utm_source"] != "newsletter" {
		t.Fatalf("utm_source=%v", me["utm_source"])
	}
	if _, ok := me["last_ip"]; !ok {
		t.Fatalf("missing last_ip: %v", me)
	}

	rr = h.do("POST", "/api/v1/auth/email/verify/request", app.ID.String(), access, map[string]any{})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("request-verify: %d %s", rr.Code, rr.Body.String())
	}
}

func TestOAuthStateSingleUse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ghtok","token_type":"bearer"}`))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"login":"once","name":"Once","avatar_url":""}`))
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"once@ex.com","primary":true,"verified":true}]`))
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
	app, err := tSvc.Create(context.Background(), "once", "Once", tenant.Settings{
		AuthRedirectURLs: []string{"https://acme.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
		Email:            tenant.EmailSettings{FromAddress: "n@acme.com", ActionBaseURL: "https://acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, challenge := pkcePair()
	req := httptest.NewRequest("GET", "/api/v1/auth/oauth/github?app_id="+app.ID.String()+
		"&redirect_uri="+url.QueryEscape("https://acme.com/cb")+
		"&code_challenge="+challenge+"&code_challenge_method=S256", nil)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	state := mustQuery(t, rr.Header().Get("Location"), "state")

	req = httptest.NewRequest("GET", "/api/v1/auth/oauth/github/callback?code=abc&state="+url.QueryEscape(state), nil)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("first callback: %d", rr.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/auth/oauth/github/callback?code=abc2&state="+url.QueryEscape(state), nil)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	// Reused/unknown state → redirect with error or 400.
	if rr.Code == http.StatusFound {
		if !strings.Contains(rr.Header().Get("Location"), "error=") {
			t.Fatalf("want error on reused state, got %s", rr.Header().Get("Location"))
		}
	} else if rr.Code != http.StatusBadRequest {
		t.Fatalf("reuse callback: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAppleRevokeEmptyAndCollect(t *testing.T) {
	sub := auth.NewAppleRevokeSubscriber(nil, nil, nil)
	appID := uuid.Must(uuid.NewV7())
	if err := sub.Handle(context.Background(), outbox.Row{AppID: &appID}, []byte(`{"user_id":"x"}`)); err != nil {
		t.Fatalf("empty tokens should no-op: %v", err)
	}
	id := uuid.Must(uuid.NewV7())
	toks := auth.CollectAppleRefreshTokens([]auth.Identity{{
		ID: id, Provider: provider.Apple, ProviderRefreshEnc: []byte("enc"),
	}}, func(enc, aad []byte) ([]byte, error) {
		return []byte("rt_apple"), nil
	})
	if len(toks) != 1 || toks[0] != "rt_apple" {
		t.Fatalf("%v", toks)
	}
}

func TestGoogleNativeHTTP(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := jwks.New(nil)
	cache.SetKeysForTest("https://www.googleapis.com/oauth2/v3/certs", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	provReg := provider.NewRegistry(nil, cache)
	srv, tSvc, _, _ := newOAuthHarness(t, provReg, config.DefaultConnections{
		GoogleOAuth: &config.GoogleOAuthDefault{ClientID: "web", ClientSecret: "s", NativeClientIDs: []string{"native"}},
	})
	app, err := tSvc.Create(context.Background(), "ggl", "G", tenant.Settings{
		Auth:  tenant.AuthSettings{Providers: []string{"google"}},
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idTok := signGoogle(t, rsaKey, "native", "sub-n", "n@ex.com", true)
	body, _ := json.Marshal(map[string]any{"id_token": idTok})
	req := httptest.NewRequest("POST", "/api/v1/auth/google", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("native google: %d %s", rr.Code, rr.Body.String())
	}

	// Google One Tap sets nonce: "not_provided" when omitted by client.
	sNonce := signGoogleWithNonce(t, rsaKey, "native", "sub-onetap", "onetap@ex.com", true, "not_provided")
	bodyNonce, _ := json.Marshal(map[string]any{"id_token": sNonce})
	reqNonce := httptest.NewRequest("POST", "/api/v1/auth/google", strings.NewReader(string(bodyNonce)))
	reqNonce.Header.Set("Content-Type", "application/json")
	reqNonce.Header.Set("X-App-ID", app.ID.String())
	rrNonce := httptest.NewRecorder()
	srv.Router().ServeHTTP(rrNonce, reqNonce)
	if rrNonce.Code != 200 {
		t.Fatalf("native google onetap nonce: %d %s", rrNonce.Code, rrNonce.Body.String())
	}

	// Test GET /api/v1/auth/config
	reqCfg := httptest.NewRequest("GET", "/api/v1/auth/config", nil)
	reqCfg.Header.Set("X-App-ID", app.ID.String())
	rrCfg := httptest.NewRecorder()
	srv.Router().ServeHTTP(rrCfg, reqCfg)
	if rrCfg.Code != 200 {
		t.Fatalf("auth config: %d %s", rrCfg.Code, rrCfg.Body.String())
	}
	var pubCfg auth.PublicConfig
	if err := json.Unmarshal(rrCfg.Body.Bytes(), &pubCfg); err != nil {
		t.Fatal(err)
	}
	if !pubCfg.Auth.PasswordEnabled {
		t.Errorf("expected password_enabled true")
	}
	if g, ok := pubCfg.Auth.Providers["google"]; !ok || !g.Enabled || g.ClientID != "web" {
		t.Errorf("unexpected google config: %+v", g)
	}
}
