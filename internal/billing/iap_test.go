package billing_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/billing/appstore"
	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

type iapHarness struct {
	srv         *server.Server
	tSvc        *tenant.Service
	iSvc        *integration.Service
	catalog     *catalog.Service
	billing     *billing.Service
	credits     *credit.Service
	authSvc     *auth.Service
	appleURL    string
	googleURL   string
	appleChain  *iapChain
	txMu        sync.Mutex
	appleTx     map[string]string // transactionID → signed JWS
	googleMu    sync.Mutex
	googlePurch map[string]playstore.ProductPurchase // token → purchase
	voided      []playstore.VoidedPurchase
	consumed    []string
}

type iapChain struct {
	rootPEM  []byte
	leafKey  *ecdsa.PrivateKey
	leafDER  []byte
	interDER []byte
	privPEM  string
}

func setupIAP(t *testing.T) *iapHarness {
	t.Helper()
	h := &iapHarness{
		appleTx:     map[string]string{},
		googlePurch: map[string]playstore.ProductPurchase{},
	}
	h.appleChain = genIAPChain(t)

	appleMux := http.NewServeMux()
	appleMux.HandleFunc("/inApps/v1/transactions/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/inApps/v1/transactions/")
		h.txMu.Lock()
		jws, ok := h.appleTx[id]
		h.txMu.Unlock()
		if !ok {
			http.Error(w, `{"errorCode":4040010}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"signedTransactionInfo": jws})
	})
	appleSrv := httptest.NewServer(appleMux)
	t.Cleanup(appleSrv.Close)
	h.appleURL = appleSrv.URL

	googleMux := http.NewServeMux()
	googleMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, ":consume") && r.Method == http.MethodPost:
			h.googleMu.Lock()
			h.consumed = append(h.consumed, path)
			h.googleMu.Unlock()
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/purchases/voidedpurchases"):
			h.googleMu.Lock()
			items := append([]playstore.VoidedPurchase{}, h.voided...)
			h.googleMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"voidedPurchases": items})
		case strings.Contains(path, "/purchases/products/") && r.Method == http.MethodGet:
			// .../products/{id}/tokens/{token}
			parts := strings.Split(path, "/")
			token := parts[len(parts)-1]
			h.googleMu.Lock()
			p, ok := h.googlePurch[token]
			h.googleMu.Unlock()
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			_ = json.NewEncoder(w).Encode(p)
		default:
			http.Error(w, "no route "+path, 404)
		}
	})
	googleSrv := httptest.NewServer(googleMux)
	t.Cleanup(googleSrv.Close)
	h.googleURL = googleSrv.URL

	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	jwtKey := bytes.Repeat([]byte{9}, 32)
	cfg := &config.Config{
		Env: "development", PublicURL: "https://api.example.com",
		AdminEnabled: true, AdminToken: adminToken,
		JWTAccessTTL: time.Minute, JWTRefreshTTL: 24 * time.Hour,
		JWTSigningKeys: []config.KeyMaterial{{ID: "dev", Key: jwtKey}},
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

	stripeClient := stripeapi.New(http.DefaultClient)
	catSvc := catalog.NewService(catalog.NewStore(database), resolver, stripeClient)
	billSvc := billing.NewService(database, billing.NewStore(database), catSvc, creditSvc, pub, resolver, iSvc, iStore, box, userStore, tSvc, stripeClient, true)

	verifier, err := appstore.NewVerifierWithRootPEM(h.appleChain.rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	appleClient := appstore.NewClient(appleSrv.Client(), verifier)
	appleClient.ProdURL = appleSrv.URL
	appleClient.Sandbox = appleSrv.URL
	googleClient := playstore.New(googleSrv.Client())
	googleClient.BaseURL = googleSrv.URL
	googleClient.TokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test"})
	billSvc.SetIAP(appleClient, googleClient, billing.NewJobStore(database), slog.New(slog.NewTextHandler(io.Discard, nil)))

	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, admin.NewAuditStore(database), tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, catSvc, stripeClient, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: auth.NewMiddleware(tokens, tSvc), Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc), AI: ai.NewHandler(creditSvc, userStore),
		Auth: auth.NewHandler(authSvc, nil), Catalog: catalog.NewHandler(catSvc), Billing: billing.NewHandler(billSvc),
		VoidedPoller: billing.NewVoidedPoller(billSvc),
	})
	billSvc.SetAuthTokens(tokens)
	h.srv, h.tSvc, h.iSvc, h.catalog, h.billing, h.credits, h.authSvc = srv, tSvc, iSvc, catSvc, billSvc, creditSvc, authSvc
	return h
}

func (h *iapHarness) createApp(t *testing.T, slug string) tenant.App {
	t.Helper()
	app, err := h.tSvc.Create(context.Background(), slug, strings.ToUpper(slug), tenant.Settings{
		AllowedOrigins:  []string{"https://" + slug + ".com"},
		AllowIAPSandbox: true,
		Auth:            tenant.AuthSettings{PasswordEnabled: true},
		Email:           tenant.EmailSettings{FromAddress: "n@" + slug + ".com", ActionBaseURL: "https://" + slug + ".com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (h *iapHarness) register(t *testing.T, app tenant.App, email string) (access string, userID uuid.UUID) {
	t.Helper()
	resp, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: email, Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Tokens.AccessToken, resp.User.ID
}

func (h *iapHarness) createAppleConn(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": "apple_iap", "name": "apple",
		"settings": map[string]any{
			"issuer_id": "issuer", "key_id": "kid", "bundle_id": "com.example.app",
		},
		"secrets": map[string]any{"private_key_pem": h.appleChain.privPEM},
	})
	req := httptest.NewRequest("POST", "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("apple conn: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return out["id"].(string)
}

func (h *iapHarness) createGoogleConn(t *testing.T) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": "google_play", "name": "play",
		"settings": map[string]any{"package_name": "com.example.app"},
		"secrets":  map[string]any{"service_account_json": `{"type":"service_account","project_id":"x"}`},
	})
	req := httptest.NewRequest("POST", "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("google conn: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return out["id"].(string)
}

func (h *iapHarness) bind(t *testing.T, appID, provider, connID string) {
	t.Helper()
	id, err := uuid.Parse(appID)
	if err != nil {
		t.Fatal(err)
	}
	app, err := h.tSvc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cfgBody, _ := json.Marshal(map[string]any{
		"settings": app.Settings,
		"integrations": map[string]any{
			provider: map[string]any{"connection_id": connID},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/admin/apps/"+appID+"/config", bytes.NewReader(cfgBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("bind %s: %d %s", provider, rr.Code, rr.Body.String())
	}
}

func (h *iapHarness) putAppleTx(t *testing.T, txID, productID, env string, userID uuid.UUID) {
	t.Helper()
	claims := map[string]any{
		"transactionId": txID, "originalTransactionId": txID,
		"bundleId": "com.example.app", "productId": productID,
		"type": "Consumable", "environment": env,
		"appAccountToken": userID.String(), "signedDate": time.Now().UnixMilli(),
	}
	jws := h.appleChain.sign(t, claims)
	h.txMu.Lock()
	h.appleTx[txID] = jws
	h.txMu.Unlock()
}

func (h *iapHarness) verifyApple(t *testing.T, access string, appID uuid.UUID, txID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"platform": "apple", "transaction_id": txID})
	req := httptest.NewRequest("POST", "/api/v1/billing/verify-iap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", appID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	return rr
}

func (h *iapHarness) verifyGoogle(t *testing.T, access string, appID uuid.UUID, productID, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"platform": "google", "product_id": productID, "purchase_token": token,
	})
	req := httptest.NewRequest("POST", "/api/v1/billing/verify-iap", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", appID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	return rr
}

func TestIAPClaimApple(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "acme")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)
	accessX, userX := h.register(t, app, "x@ex.com")
	accessY, _ := h.register(t, app, "y@ex.com")

	appleID := "credits_100"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack100", Name: "100", Credits: 100, PriceAmount: 0, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}
	txID := "tx_claim_1"
	h.putAppleTx(t, txID, appleID, "Production", userX)

	rr := h.verifyApple(t, accessX, app.ID, txID)
	if rr.Code != 200 {
		t.Fatalf("x first: %d %s", rr.Code, rr.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res["already_processed"] == true {
		t.Fatal("first claim should not be already_processed")
	}
	if int64(res["credits_added"].(float64)) != 100 {
		t.Fatalf("credits: %v", res["credits_added"])
	}

	rr = h.verifyApple(t, accessY, app.ID, txID)
	if rr.Code != 409 {
		t.Fatalf("y claim want 409, got %d %s", rr.Code, rr.Body.String())
	}

	rr = h.verifyApple(t, accessX, app.ID, txID)
	if rr.Code != 200 {
		t.Fatalf("x retry: %d %s", rr.Code, rr.Body.String())
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res["already_processed"] != true {
		t.Fatalf("want already_processed, got %v", res)
	}
}

func TestIAPClaimGoogle(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "acme")
	conn := h.createGoogleConn(t)
	h.bind(t, app.ID.String(), "google_play", conn)
	accessX, userX := h.register(t, app, "gx@ex.com")
	accessY, _ := h.register(t, app, "gy@ex.com")

	gID := "credits_50"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack50", Name: "50", Credits: 50, PriceAmount: 0, Currency: "usd",
		GoogleProductID: &gID,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "purchase-token-1"
	h.googleMu.Lock()
	h.googlePurch[token] = playstore.ProductPurchase{
		PurchaseState: 0, ConsumptionState: 0, OrderID: "GPA.1",
		ObfuscatedExternalAccountID: userX.String(), PurchaseToken: token,
	}
	h.googleMu.Unlock()

	rr := h.verifyGoogle(t, accessX, app.ID, gID, token)
	if rr.Code != 200 {
		t.Fatalf("x first: %d %s", rr.Code, rr.Body.String())
	}
	rr = h.verifyGoogle(t, accessY, app.ID, gID, token)
	if rr.Code != 409 {
		t.Fatalf("y claim want 409, got %d %s", rr.Code, rr.Body.String())
	}
	rr = h.verifyGoogle(t, accessX, app.ID, gID, token)
	if rr.Code != 200 {
		t.Fatalf("x retry: %d", rr.Code)
	}
	var res map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res["already_processed"] != true {
		t.Fatalf("want already_processed: %v", res)
	}
	time.Sleep(50 * time.Millisecond) // consume goroutine
}

func TestAppleRefundAndReversal(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "acme")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)
	access, userID := h.register(t, app, "r@ex.com")

	appleID := "credits_100"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack100", Name: "100", Credits: 100, PriceAmount: 0, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}
	txID := "tx_refund_1"
	h.putAppleTx(t, txID, appleID, "Production", userID)
	rr := h.verifyApple(t, access, app.ID, txID)
	if rr.Code != 200 {
		t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
	}
	_, err = h.credits.Consume(context.Background(), app.ID, userID, 40, "spend1", "feat", "user:"+userID.String(), nil)
	if err != nil {
		t.Fatal(err)
	}

	signedTx := h.appleChain.sign(t, map[string]any{
		"transactionId": txID, "bundleId": "com.example.app", "productId": appleID,
		"type": "Consumable", "environment": "Production",
	})
	notif := h.appleChain.sign(t, map[string]any{
		"notificationType": "REFUND", "notificationUUID": "notif-ref-1",
		"data": map[string]any{
			"bundleId": "com.example.app", "environment": "Production",
			"signedTransactionInfo": signedTx,
		},
	})
	body, _ := json.Marshal(map[string]string{"signedPayload": notif})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	wrr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(wrr, req)
	if wrr.Code != 200 {
		t.Fatalf("refund webhook: %d %s", wrr.Code, wrr.Body.String())
	}
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != -40 {
		t.Fatalf("after refund want -40, got %d", bal.Balance)
	}
	_, err = h.credits.Consume(context.Background(), app.ID, userID, 1, "blocked", "feat", "user:"+userID.String(), nil)
	if err == nil {
		t.Fatal("want insufficient while negative")
	}

	// duplicate REFUND
	req2 := httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	h.srv.Router().ServeHTTP(httptest.NewRecorder(), req2)
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != -40 {
		t.Fatalf("dup refund changed balance: %d", bal.Balance)
	}

	revNotif := h.appleChain.sign(t, map[string]any{
		"notificationType": "REFUND_REVERSED", "notificationUUID": "notif-rev-1",
		"data": map[string]any{
			"bundleId": "com.example.app", "environment": "Production",
			"signedTransactionInfo": signedTx,
		},
	})
	revBody, _ := json.Marshal(map[string]string{"signedPayload": revNotif})
	req = httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(revBody))
	req.Header.Set("Content-Type", "application/json")
	wrr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(wrr, req)
	if wrr.Code != 200 {
		t.Fatalf("reversal: %d %s", wrr.Code, wrr.Body.String())
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 60 {
		t.Fatalf("after reversal want 60, got %d", bal.Balance)
	}
}

func TestGoogleVoidedRefund(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "acme")
	conn := h.createGoogleConn(t)
	h.bind(t, app.ID.String(), "google_play", conn)
	access, userID := h.register(t, app, "gv@ex.com")

	gID := "credits_80"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack80", Name: "80", Credits: 80, PriceAmount: 0, Currency: "usd",
		GoogleProductID: &gID,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "void-token-1"
	h.googleMu.Lock()
	h.googlePurch[token] = playstore.ProductPurchase{
		PurchaseState: 0, OrderID: "GPA.void", PurchaseToken: token,
		ObfuscatedExternalAccountID: userID.String(),
	}
	h.googleMu.Unlock()
	rr := h.verifyGoogle(t, access, app.ID, gID, token)
	if rr.Code != 200 {
		t.Fatalf("verify: %d %s", rr.Code, rr.Body.String())
	}
	// Wait for async Play consume so it does not race the voided poller HTTP client.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.googleMu.Lock()
		n := len(h.consumed)
		h.googleMu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	h.googleMu.Lock()
	h.voided = []playstore.VoidedPurchase{{
		PurchaseToken: token, VoidedTimeMillis: fmt.Sprintf("%d", time.Now().UnixMilli()),
		OrderID: "GPA.void",
	}}
	h.googleMu.Unlock()

	billing.NewVoidedPoller(h.billing).Tick(context.Background())
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 0 {
		t.Fatalf("after voided want 0, got %d", bal.Balance)
	}
	// idempotent
	billing.NewVoidedPoller(h.billing).Tick(context.Background())
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 0 {
		t.Fatalf("second poll changed balance: %d", bal.Balance)
	}
}

func TestAppleWebhookBadJWS(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "acme")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)

	body, _ := json.Marshal(map[string]string{"signedPayload": "not.a.jws"})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func genIAPChain(t *testing.T) *iapChain {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "IAP Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, BasicConstraintsValid: true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCrt, _ := x509.ParseCertificate(rootDER)
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	interKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	interTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "IAP Inter"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, rootCrt, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	interCrt, _ := x509.ParseCertificate(interDER)

	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "IAP Leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, interCrt, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return &iapChain{
		rootPEM: rootPEM, leafKey: leafKey, leafDER: leafDER, interDER: interDER,
		privPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
	}
}

func (c *iapChain) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	tok.Header["x5c"] = []string{
		base64.StdEncoding.EncodeToString(c.leafDER),
		base64.StdEncoding.EncodeToString(c.interDER),
	}
	s, err := tok.SignedString(c.leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
