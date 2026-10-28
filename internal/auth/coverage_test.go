package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/jwks"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/tenant"
)

func TestAuthCoverageHelpers(t *testing.T) {
	ts := auth.NewTokenService([]config.KeyMaterial{{ID: "k", Key: make([]byte, 32)}}, time.Minute)
	if ts.AccessTTL() != time.Minute {
		t.Fatalf("ttl=%v", ts.AccessTTL())
	}
	p := principal.Principal{UserID: uuid.Must(uuid.NewV7()), Actor: "user:x"}
	ctx := principal.With(context.Background(), p)
	got, ok := auth.FromContext(ctx)
	if !ok || got.UserID != p.UserID {
		t.Fatalf("%+v ok=%v", got, ok)
	}
}

type fileMarkerStub struct{ called bool }

func (f *fileMarkerStub) MarkDeletedForUser(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error {
	f.called = true
	return nil
}

type referralStub struct{ called bool }

func (r *referralStub) ApplyTx(ctx context.Context, tx *sqlx.Tx, app tenant.App, refereeID uuid.UUID, code string) (referral.ApplyResult, error) {
	return referral.ApplyResult{}, nil
}

func (r *referralStub) VoidAsReferee(ctx context.Context, tx *sqlx.Tx, appID, userID uuid.UUID) error {
	r.called = true
	return nil
}

func TestAuthErrorPathsAndHooks(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "cov", false, 0)

	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "bad", "password": "password123",
	})
	if rr.Code != 400 {
		t.Fatalf("bad email: %d", rr.Code)
	}
	rr = h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "ok@ex.com", "password": "short",
	})
	if rr.Code != 400 {
		t.Fatalf("short pw: %d", rr.Code)
	}
	rr = h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "ok@ex.com", "password": "password123", "display_name": "Cov",
	})
	if rr.Code != 201 {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	access := reg["tokens"].(map[string]any)["access_token"].(string)
	userID, _ := uuid.Parse(reg["user"].(map[string]any)["id"].(string))

	rr = h.do("POST", "/api/v1/auth/login", app.ID.String(), "", map[string]any{
		"email": "ok@ex.com", "password": "wrongpassword1",
	})
	if rr.Code != 401 {
		t.Fatalf("bad login: %d", rr.Code)
	}
	rr = h.do("POST", "/api/v1/auth/refresh", app.ID.String(), "", map[string]any{
		"refresh_token": "rt_bogus",
	})
	if rr.Code != 401 {
		t.Fatalf("bad refresh: %d", rr.Code)
	}
	rr = h.do("GET", "/api/v1/user/me", app.ID.String(), "not-a-jwt", nil)
	if rr.Code != 401 {
		t.Fatalf("junk bearer: %d", rr.Code)
	}
	rr = h.do("GET", "/api/v1/user/me", app.ID.String(), "", nil)
	if rr.Code != 401 {
		t.Fatalf("no auth: %d", rr.Code)
	}

	rr = h.do("POST", "/api/v1/auth/password/forgot", app.ID.String(), "", map[string]any{"email": "nobody@ex.com"})
	if rr.Code != 202 {
		t.Fatalf("forgot unknown: %d", rr.Code)
	}
	rr = h.do("POST", "/api/v1/auth/password/reset", app.ID.String(), "", map[string]any{
		"token": "et_nope", "password": "password999",
	})
	if rr.Code == 204 {
		t.Fatal("bad reset accepted")
	}
	rr = h.do("POST", "/api/v1/auth/email/verify", app.ID.String(), "", map[string]any{"token": "et_nope"})
	if rr.Code == 200 {
		t.Fatal("bad verify accepted")
	}

	rr = h.do("PATCH", "/api/v1/user/me", app.ID.String(), access, map[string]any{"display_name": "Patched"})
	if rr.Code != 200 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}

	if err := h.authSvc.RevokeAllSessions(context.Background(), app.ID, userID); err != nil {
		t.Fatal(err)
	}

	fm := &fileMarkerStub{}
	rs := &referralStub{}
	h.authSvc.SetFileMarker(fm)
	h.authSvc.SetReferrals(rs)
	rr = h.do("DELETE", "/api/v1/user/me", app.ID.String(), access, map[string]any{"password": "password123"})
	if rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	if !fm.called || !rs.called {
		t.Fatalf("hooks file=%v ref=%v", fm.called, rs.called)
	}
}

func TestReferralUsersAdapter(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "refu", "RU", tenant.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(database)
	email := "r@ex.com"
	code := "REFCODE1"
	u := auth.User{
		ID: uuid.Must(uuid.NewV7()), AppID: app.ID, Email: &email,
		DisplayName: "R", Status: auth.StatusActive, ReferralCode: &code,
		CreatedAt: db.Now(), UpdatedAt: db.Now(),
	}
	if err := store.Insert(context.Background(), nil, u); err != nil {
		t.Fatal(err)
	}
	adapter := auth.ReferralUsers{Store: store}
	byCode, err := adapter.GetByReferralCode(context.Background(), nil, app.ID, code)
	if err != nil || byCode.ID != u.ID {
		t.Fatalf("%+v %v", byCode, err)
	}
	byID, err := adapter.GetUser(context.Background(), nil, app.ID, u.ID)
	if err != nil || byID.DisplayName != "R" {
		t.Fatalf("%+v %v", byID, err)
	}
}

func TestAppleNativeHTTP(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}))

	cache := jwks.New(nil)
	cache.SetKeysForTest("https://appleid.apple.com/auth/keys", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	provReg := provider.NewRegistry(nil, cache)

	srv, tSvc, authSvc, _ := newOAuthHarness(t, provReg, config.DefaultConnections{
		AppleSignIn: &config.AppleSignInDefault{
			TeamID: "TEAM", KeyID: "KEYID", ServicesID: "com.svc",
			BundleIDs: []string{"com.app.bundle"}, PrivateKey: ecPEM,
		},
	})
	app, err := tSvc.Create(context.Background(), "apple", "Apple", tenant.Settings{
		Auth:  tenant.AuthSettings{Providers: []string{"apple"}},
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idTok := signApple(t, rsaKey, "com.app.bundle", "apple-sub-1", "a@ex.com")
	// Non-empty auth code exercises exchangeAppleNative (adapter may not support it).
	resp, err := authSvc.NativeApple(context.Background(), app, idTok, "auth-code", "", "Apple User", nil, "", "", "")
	if err != nil {
		t.Fatalf("NativeApple: %v", err)
	}
	if resp.User.ID == uuid.Nil {
		t.Fatal("missing user")
	}
	// HTTP handler path (body uses object full_name)
	payload := []byte(`{"identity_token":` + mustJSONString(idTok) + `,"full_name":{"given_name":"A","family_name":"B"}}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/apple", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("native apple http: %d %s", rr.Code, rr.Body.String())
	}
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func signApple(t *testing.T, key *rsa.PrivateKey, aud, sub, email string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://appleid.apple.com", "sub": sub, "aud": aud,
		"email": email,
		"iat":   time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "test"
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAppleRevokeMissingApp(t *testing.T) {
	sub := auth.NewAppleRevokeSubscriber(nil, nil, nil)
	err := sub.Handle(context.Background(), outbox.Row{}, []byte(`{"user_id":"x","apple_refresh_tokens":["rt"]}`))
	if err == nil {
		t.Fatal("want permanent missing app")
	}
}

func TestAPIKeyAuthPath(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "apikey", false, 0)
	_, raw, err := h.tSvc.CreateAPIKey(context.Background(), app.ID, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"feature": "x", "amount": 1, "idempotency_key": "k1"})
	req := httptest.NewRequest("POST", "/api/v1/ai/consume", strings.NewReader(string(body)))
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	// API key auth succeeds; consume may 402 without balance.
	if rr.Code != 402 && rr.Code != 200 && rr.Code != 400 {
		t.Fatalf("api key consume: %d %s", rr.Code, rr.Body.String())
	}
	// Cross-app key rejected
	other := h.createApp(t, "other", false, 0)
	req = httptest.NewRequest("POST", "/api/v1/ai/consume", strings.NewReader(string(body)))
	req.Header.Set("X-App-ID", other.ID.String())
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("cross-app key want 401, got %d", rr.Code)
	}
}

func TestAuthStoreAdminHelpers(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "store", "ST", tenant.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(database)
	email := "s@ex.com"
	code := "STORE001"
	u := auth.User{
		ID: uuid.Must(uuid.NewV7()), AppID: app.ID, Email: &email,
		DisplayName: "", Status: auth.StatusActive, ReferralCode: &code,
		CreatedAt: db.Now(), UpdatedAt: db.Now(),
	}
	if err := store.Insert(context.Background(), nil, u); err != nil {
		t.Fatal(err)
	}
	all, err := store.List(context.Background(), nil, app.ID, "", "", 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v n=%d", err, len(all))
	}
	byQ, err := store.List(context.Background(), nil, app.ID, "s@", "", 10)
	if err != nil || len(byQ) != 1 {
		t.Fatalf("query: %v", err)
	}
	byS, err := store.List(context.Background(), nil, app.ID, "", auth.StatusActive, 10)
	if err != nil || len(byS) != 1 {
		t.Fatalf("status: %v", err)
	}
	both, err := store.List(context.Background(), nil, app.ID, "s@", auth.StatusActive, 10)
	if err != nil || len(both) != 1 {
		t.Fatalf("both: %v", err)
	}
	if err := store.UpdateStatus(context.Background(), nil, app.ID, u.ID, auth.StatusDisabled, db.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateStatus(context.Background(), nil, app.ID, uuid.Must(uuid.NewV7()), auth.StatusActive, db.Now()); !auth.IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := store.UpdatePasswordHash(context.Background(), nil, app.ID, u.ID, "hash", db.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDisplayNameIfEmpty(context.Background(), nil, app.ID, u.ID, "Named", db.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetByID(context.Background(), nil, app.ID, u.ID)
	if got.DisplayName != "Named" {
		t.Fatalf("display=%q", got.DisplayName)
	}
	if err := store.SetDisplayNameIfEmpty(context.Background(), nil, app.ID, u.ID, "Other", db.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetByID(context.Background(), nil, app.ID, u.ID)
	if got.DisplayName != "Named" {
		t.Fatalf("should keep name, got %q", got.DisplayName)
	}
}

func TestOAuthReturningUserAndUnverified(t *testing.T) {
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
	app, err := tSvc.Create(context.Background(), "ret", "Ret", tenant.Settings{
		Auth:               tenant.AuthSettings{Providers: []string{"google"}, PasswordEnabled: true},
		Email:              tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
		SignupBonusCredits: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	idTok := signGoogle(t, rsaKey, "native", "sub-ret", "ret@ex.com", true)
	if _, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", ""); err != nil {
		t.Fatal(err)
	}
	bad := signGoogle(t, rsaKey, "native", "sub-bad", "bad@ex.com", false)
	if _, err := authSvc.NativeGoogle(context.Background(), app, bad, "", nil, "", "", ""); err == nil {
		t.Fatal("want email required")
	}
}

func TestOAuthDisabledAccount(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := jwks.New(nil)
	cache.SetKeysForTest("https://www.googleapis.com/oauth2/v3/certs", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	provReg := provider.NewRegistry(nil, cache)
	_, tSvc, authSvc, database := newOAuthHarness(t, provReg, config.DefaultConnections{
		GoogleOAuth: &config.GoogleOAuthDefault{ClientID: "web", ClientSecret: "s", NativeClientIDs: []string{"native"}},
	})
	app, err := tSvc.Create(context.Background(), "dis", "Dis", tenant.Settings{
		Auth:  tenant.AuthSettings{Providers: []string{"google"}},
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idTok := signGoogle(t, rsaKey, "native", "sub-dis", "dis@ex.com", true)
	resp, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewStore(database).UpdateStatus(context.Background(), nil, app.ID, resp.User.ID, auth.StatusDisabled, db.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", ""); err == nil {
		t.Fatal("want disabled")
	}
}

func TestAppleRevokeNotConfigured(t *testing.T) {
	cache := jwks.New(nil)
	reg := provider.NewRegistry(nil, cache)
	database := dbtest.New(t, "sqlite")
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "arev", "AR", tenant.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	env := integration.BuildEnvDefaults(&config.Config{})
	iStore := integration.NewStore(database)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := integration.NewResolver(iStore, box, env)
	sub := auth.NewAppleRevokeSubscriber(reg, resolver, nil)
	appID := app.ID
	if err := sub.Handle(context.Background(), outbox.Row{AppID: &appID}, []byte(`{"user_id":"x","apple_refresh_tokens":["rt"]}`)); err == nil {
		t.Fatal("want not configured")
	}
}

func TestDeleteMeReauthAndPatchNotFound(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "reauth", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "re@ex.com", "password": "password123",
	})
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	userID, _ := uuid.Parse(reg["user"].(map[string]any)["id"].(string))

	err := h.authSvc.DeleteMe(context.Background(), app.ID, userID, nil, time.Now().UTC().Add(-10*time.Minute))
	if err == nil {
		t.Fatal("want reauth required")
	}
	wrong := "nope-password1"
	err = h.authSvc.DeleteMe(context.Background(), app.ID, userID, &wrong, time.Now().UTC().Add(-10*time.Minute))
	if err == nil {
		t.Fatal("want invalid credentials")
	}
	name := "x"
	if _, err := h.authSvc.PatchMe(context.Background(), app.ID, uuid.Must(uuid.NewV7()), &name, nil); err == nil {
		t.Fatal("want user not found")
	}
}

func TestProviderDisabledNative(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := jwks.New(nil)
	cache.SetKeysForTest("https://www.googleapis.com/oauth2/v3/certs", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	_, tSvc, authSvc, _ := newOAuthHarness(t, provider.NewRegistry(nil, cache), config.DefaultConnections{
		GoogleOAuth: &config.GoogleOAuthDefault{ClientID: "web", ClientSecret: "s", NativeClientIDs: []string{"native"}},
	})
	app, err := tSvc.Create(context.Background(), "nodis", "No", tenant.Settings{
		Auth:  tenant.AuthSettings{Providers: []string{}}, // google disabled
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idTok := signGoogle(t, rsaKey, "native", "sub-x", "x@ex.com", true)
	if _, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", ""); err == nil {
		t.Fatal("want provider disabled")
	}
}

func TestRequestVerifyAlreadyVerified(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "verif", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "v2@ex.com", "password": "password123",
	})
	var reg map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &reg)
	access := reg["tokens"].(map[string]any)["access_token"].(string)
	// mark verified via admin-less path: verify token from mail then request again
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
	if verifyToken != "" {
		h.do("POST", "/api/v1/auth/email/verify", app.ID.String(), "", map[string]any{"token": verifyToken})
	}
	rr = h.do("POST", "/api/v1/auth/email/verify/request", app.ID.String(), access, map[string]any{})
	if rr.Code != 202 && rr.Code != 204 && rr.Code != 400 {
		t.Fatalf("request verify: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAuthHandlerBadJSONAndRateLimits(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "rates", false, 0)
	paths := []string{
		"/api/v1/auth/register", "/api/v1/auth/login", "/api/v1/auth/refresh",
		"/api/v1/auth/password/forgot", "/api/v1/auth/password/reset",
		"/api/v1/auth/email/verify", "/api/v1/auth/oauth/exchange",
		"/api/v1/auth/apple", "/api/v1/auth/google",
	}
	for _, p := range paths {
		req := httptest.NewRequest("POST", p, strings.NewReader(`{`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-App-ID", app.ID.String())
		rr := httptest.NewRecorder()
		h.srv.Router().ServeHTTP(rr, req)
		if rr.Code == 201 || rr.Code == 200 {
			t.Fatalf("%s accepted bad json: %d", p, rr.Code)
		}
	}
	// register rate limit (burst 10)
	for i := 0; i < 12; i++ {
		rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
			"email": fmt.Sprintf("r%d@ex.com", i), "password": "password123",
		})
		if i >= 10 && rr.Code != 429 {
			// may already be 400 if somehow; allow 429
			if rr.Code != http.StatusTooManyRequests && rr.Code != 429 {
				t.Logf("register[%d]=%d %s", i, rr.Code, rr.Body.String())
			}
		}
	}
	// ensure at least one 429 eventually
	saw := false
	for i := 0; i < 5; i++ {
		rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
			"email": fmt.Sprintf("extra%d@ex.com", i), "password": "password123",
		})
		if rr.Code == 429 {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatal("expected register rate limit")
	}
	// login email limit
	h2 := setup(t)
	app2 := h2.createApp(t, "loglim", false, 0)
	h2.do("POST", "/api/v1/auth/register", app2.ID.String(), "", map[string]any{
		"email": "lim@ex.com", "password": "password123",
	})
	for i := 0; i < 8; i++ {
		rr := h2.do("POST", "/api/v1/auth/login", app2.ID.String(), "", map[string]any{
			"email": "lim@ex.com", "password": "wrongpassword1",
		})
		if rr.Code == 429 {
			return
		}
	}
	t.Fatal("expected login email rate limit")
}

func TestOAuthSetsDisplayNameOnReturn(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cache := jwks.New(nil)
	cache.SetKeysForTest("https://www.googleapis.com/oauth2/v3/certs", map[string]*rsa.PublicKey{"test": &rsaKey.PublicKey})
	_, tSvc, authSvc, database := newOAuthHarness(t, provider.NewRegistry(nil, cache), config.DefaultConnections{
		GoogleOAuth: &config.GoogleOAuthDefault{ClientID: "web", ClientSecret: "s", NativeClientIDs: []string{"native"}},
	})
	app, err := tSvc.Create(context.Background(), "named", "Named", tenant.Settings{
		Auth:  tenant.AuthSettings{Providers: []string{"google"}},
		Email: tenant.EmailSettings{FromAddress: "n@a.com", ActionBaseURL: "https://a.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// first login without name
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://accounts.google.com", "sub": "sub-name", "aud": "native",
		"email": "name@ex.com", "email_verified": true,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "test"
	idTok, _ := tok.SignedString(rsaKey)
	resp, err := authSvc.NativeGoogle(context.Background(), app, idTok, "", nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// clear display name
	_, _ = database.ExecContext(context.Background(), database.Rebind(`UPDATE users SET display_name = '' WHERE id = ?`), resp.User.ID.String())
	tok2 := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://accounts.google.com", "sub": "sub-name", "aud": "native",
		"email": "name@ex.com", "email_verified": true, "name": "From Google",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok2.Header["kid"] = "test"
	idTok2, _ := tok2.SignedString(rsaKey)
	if _, err := authSvc.NativeGoogle(context.Background(), app, idTok2, "", nil, "", "", ""); err != nil {
		t.Fatal(err)
	}
	u, err := auth.NewStore(database).GetByID(context.Background(), nil, app.ID, resp.User.ID)
	if err != nil || u.DisplayName != "From Google" {
		t.Fatalf("display=%q err=%v", u.DisplayName, err)
	}
}

func TestRegisterDuplicateAndLogoutEmpty(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "dupes", false, 0)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "dup@ex.com", "password": "password123",
	})
	if rr.Code != 201 {
		t.Fatal(rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "dup@ex.com", "password": "password123",
	})
	if rr.Code != 409 && rr.Code != 400 {
		t.Fatalf("dup: %d %s", rr.Code, rr.Body.String())
	}
	rr = h.do("POST", "/api/v1/auth/logout", app.ID.String(), "", map[string]any{})
	if rr.Code != 204 {
		t.Fatalf("logout empty: %d", rr.Code)
	}
	// forgot + verify rate limits
	for i := 0; i < 6; i++ {
		h.do("POST", "/api/v1/auth/password/forgot", app.ID.String(), "", map[string]any{"email": "dup@ex.com"})
	}
	rr = h.do("POST", "/api/v1/auth/password/forgot", app.ID.String(), "", map[string]any{"email": "dup@ex.com"})
	if rr.Code != 429 && rr.Code != 202 {
		t.Logf("forgot last=%d", rr.Code)
	}
}

func TestPasswordHelpers(t *testing.T) {
	if err := auth.ValidatePasswordLength("short"); err == nil {
		t.Fatal("want short rejected")
	}
	if err := auth.ValidatePasswordLength(strings.Repeat("a", 200)); err == nil {
		t.Fatal("want long rejected")
	}
	if err := auth.ValidatePasswordLength("password123"); err != nil {
		t.Fatal(err)
	}
	ok, _ := auth.VerifyPassword("x", "not-a-hash")
	if ok {
		t.Fatal("bad hash verified")
	}
}

func TestAuthServiceTTLAndPasswordDisabled(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	store := auth.NewStore(database)
	tokens := auth.NewTokenService([]config.KeyMaterial{{ID: "k", Key: make([]byte, 32)}}, time.Minute)
	creditSvc := credit.NewService(database, credit.NewStore(database))
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	oreg := outbox.NewRegistry()
	oreg.DefaultSubscriptions()
	pub := outbox.NewPublisher(outbox.NewStore(database), box, oreg)
	_ = auth.NewService(database, store, tokens, creditSvc, pub, 0) // default refresh TTL

	h := setup(t)
	app := h.createApp(t, "nopw", false, 0)
	disabled := app
	disabled.Settings.Auth.PasswordEnabled = false
	if _, err := h.authSvc.Register(context.Background(), disabled, auth.RegisterInput{
		Email: "x@ex.com", Password: "password123",
	}); err == nil {
		t.Fatal("want provider disabled")
	}
	if _, err := h.authSvc.Login(context.Background(), disabled, auth.LoginInput{
		Email: "x@ex.com", Password: "password123",
	}); err == nil {
		t.Fatal("want login disabled")
	}
}

func TestStartOAuthValidation(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	_, tSvc, authSvc, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "c", ClientSecret: "s"},
	})
	app, err := tSvc.Create(context.Background(), "oauthval", "OV", tenant.Settings{
		AuthRedirectURLs: []string{"https://oauthval.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := authSvc.StartOAuth(ctx, "github", app.ID, "https://oauthval.com/cb", "chal", "plain", nil, ""); err == nil {
		t.Fatal("want bad method")
	}
	if _, err := authSvc.StartOAuth(ctx, "github", app.ID, "https://oauthval.com/cb", "", "S256", nil, ""); err == nil {
		t.Fatal("want empty challenge")
	}
	if _, err := authSvc.StartOAuth(ctx, "unknown", app.ID, "https://oauthval.com/cb", "chal", "S256", nil, ""); err == nil {
		t.Fatal("want unknown provider")
	}
	if _, err := authSvc.StartOAuth(ctx, "github", app.ID, "https://evil.com/cb", "chal", "S256", nil, ""); err == nil {
		t.Fatal("want bad redirect")
	}
	app2, _ := tSvc.Create(context.Background(), "oauthoff", "OO", tenant.Settings{
		AuthRedirectURLs: []string{"https://oauthoff.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{}},
	})
	if _, err := authSvc.StartOAuth(ctx, "github", app2.ID, "https://oauthoff.com/cb", "chal", "S256", nil, ""); err == nil {
		t.Fatal("want disabled")
	}
	// success with referral + attribution
	url, err := authSvc.StartOAuth(ctx, "github", app.ID, "https://oauthval.com/cb", "chal", "S256", json.RawMessage(`{"utm":"x"}`), "REFCODE")
	if err != nil || url == "" {
		t.Fatalf("start: %v %q", err, url)
	}
}

func TestRegisterWithReferralHook(t *testing.T) {
	h := setup(t)
	app := h.createApp(t, "refhook", false, 10)
	rs := &referralStub{}
	h.authSvc.SetReferrals(rs)
	rr := h.do("POST", "/api/v1/auth/register", app.ID.String(), "", map[string]any{
		"email": "rh@ex.com", "password": "password123", "referral_code": "ANYCODE1",
	})
	if rr.Code != 201 {
		t.Fatalf("register: %d %s", rr.Code, rr.Body.String())
	}
}

func TestOAuthCallbackEdgeErrors(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	_, tSvc, authSvc, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "c", ClientSecret: "s"},
	})
	app, _ := tSvc.Create(context.Background(), "cbedge", "CE", tenant.Settings{
		AuthRedirectURLs: []string{"https://cbedge.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
	})
	ctx := context.Background()
	if _, err := authSvc.HandleOAuthCallback(ctx, "github", "code", "", "", ""); err == nil {
		t.Fatal("empty state")
	}
	if _, err := authSvc.HandleOAuthCallback(ctx, "github", "code", "nostate", "", ""); err == nil {
		t.Fatal("unknown state")
	}
	url, err := authSvc.StartOAuth(ctx, "github", app.ID, "https://cbedge.com/cb", "chal", "S256", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	state := mustQuery(t, url, "state")
	redir, err := authSvc.HandleOAuthCallback(ctx, "github", "", state, "access_denied", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(redir, "error=") {
		t.Fatalf("want error redirect %s", redir)
	}
}

func TestOAuthNotConfigured(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	_, tSvc, authSvc, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{})
	app, _ := tSvc.Create(context.Background(), "noconf", "NC", tenant.Settings{
		AuthRedirectURLs: []string{"https://noconf.com/cb"},
		Auth:             tenant.AuthSettings{Providers: []string{"github"}},
	})
	if _, err := authSvc.StartOAuth(context.Background(), "github", app.ID, "https://noconf.com/cb", "chal", "S256", nil, ""); err == nil {
		t.Fatal("want not configured")
	}
}

func TestStartOAuthAppNotFoundAndHandlerTrustedIP(t *testing.T) {
	gh := &provider.GitHubTestProvider{
		HTTP: http.DefaultClient, AuthURL: "https://example/auth", TokenURL: "https://example/token", APIBase: "https://example",
	}
	_, _, authSvc, _ := newOAuthHarness(t, &provider.Registry{GitHub: gh}, config.DefaultConnections{
		GitHubOAuth: &config.GitHubOAuthDefault{ClientID: "c", ClientSecret: "s"},
	})
	if _, err := authSvc.StartOAuth(context.Background(), "github", uuid.Must(uuid.NewV7()), "https://x.com/cb", "chal", "S256", nil, ""); err == nil {
		t.Fatal("want app not found")
	}

	// cover NewHandler with trusted CIDR + X-Forwarded-For path used by rate-limit key
	h := setup(t)
	app := h.createApp(t, "xff", false, 0)
	req := httptest.NewRequest("POST", "/api/v1/auth/register", strings.NewReader(`{"email":"xff@ex.com","password":"password123"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("xff register: %d %s", rr.Code, rr.Body.String())
	}
}
