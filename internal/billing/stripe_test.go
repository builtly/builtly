package billing_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
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

const (
	adminToken = "test-admin-token-at-least-32-chars!!"
	whsec      = "whsec_test_secret"
)

type stripeHarness struct {
	srv       *server.Server
	tSvc      *tenant.Service
	iSvc      *integration.Service
	catalog   *catalog.Service
	billing   *billing.Service
	credits   *credit.Service
	authSvc   *auth.Service
	stripeURL string
	accountID atomic.Value // string
	priceOK   bool
}

func setupStripe(t *testing.T) *stripeHarness {
	t.Helper()
	h := &stripeHarness{priceOK: true}
	h.accountID.Store("acct_test_1")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/account", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q}`, h.accountID.Load().(string))))
	})
	mux.HandleFunc("/v1/prices/", func(w http.ResponseWriter, r *http.Request) {
		if !h.priceOK {
			http.Error(w, `{"error":{"message":"no"}}`, 404)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/prices/")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"active":true,"type":"one_time","currency":"usd"}`, id)))
	})
	mux.HandleFunc("/v1/checkout/sessions", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cs := "cs_test_" + r.Header.Get("Idempotency-Key")
		if len(cs) > 40 {
			cs = cs[:40]
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"url":"https://checkout.stripe.com/c/pay/%s"}`, cs, cs)))
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	h.stripeURL = upstream.URL

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

	stripeClient := stripeapi.New(upstream.Client())
	stripeClient.BaseURL = upstream.URL
	catSvc := catalog.NewService(catalog.NewStore(database), resolver, stripeClient)
	billSvc := billing.NewService(database, billing.NewStore(database), catSvc, creditSvc, pub, resolver, iSvc, iStore, box, userStore, tSvc, stripeClient, true)
	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, admin.NewAuditStore(database), tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, catSvc, stripeClient, nil)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: auth.NewMiddleware(tokens, tSvc), Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc), AI: ai.NewHandler(creditSvc, userStore),
		Auth: auth.NewHandler(authSvc, nil), Catalog: catalog.NewHandler(catSvc), Billing: billing.NewHandler(billSvc),
	})
	h.srv, h.tSvc, h.iSvc, h.catalog, h.billing, h.credits, h.authSvc = srv, tSvc, iSvc, catSvc, billSvc, creditSvc, authSvc
	return h
}

func (h *stripeHarness) createApp(t *testing.T, slug string) tenant.App {
	t.Helper()
	app, err := h.tSvc.Create(context.Background(), slug, strings.ToUpper(slug), tenant.Settings{
		AllowedOrigins: []string{"https://" + slug + ".com", "http://localhost:3000"},
		Auth:           tenant.AuthSettings{PasswordEnabled: true},
		Email:          tenant.EmailSettings{FromAddress: "n@" + slug + ".com", ActionBaseURL: "https://" + slug + ".com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (h *stripeHarness) createStripeConn(t *testing.T, name, secret string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"provider": "stripe", "name": name,
		"settings": map[string]any{"livemode": false},
		"secrets":  map[string]any{"secret_key": secret, "webhook_secrets": []string{whsec}},
	})
	req := httptest.NewRequest("POST", "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create conn: %d %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return out["id"].(string)
}

func (h *stripeHarness) bindStripe(t *testing.T, appID, connID string) {
	t.Helper()
	id, err := uuid.Parse(appID)
	if err != nil {
		t.Fatal(err)
	}
	app, err := h.tSvc.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	cfgBody, err := json.Marshal(map[string]any{
		"settings": app.Settings,
		"integrations": map[string]any{
			"stripe": map[string]any{"connection_id": connID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/api/v1/admin/apps/"+appID+"/config", bytes.NewReader(cfgBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("bind: %d %s", rr.Code, rr.Body.String())
	}
}

func (h *stripeHarness) register(t *testing.T, app tenant.App, email string) (access string, userID uuid.UUID) {
	t.Helper()
	resp, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: email, Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Tokens.AccessToken, resp.User.ID
}

func signStripe(t *testing.T, body []byte, secret string, ts time.Time) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	payload := strconv.FormatInt(ts.Unix(), 10) + "." + string(body)
	_, _ = mac.Write([]byte(payload))
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func (h *stripeHarness) postWebhook(t *testing.T, connID string, body []byte, sig string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/webhooks/stripe/"+connID, bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	return rr
}

func TestStripeSignature(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "acme")
	conn := h.createStripeConn(t, "stripe-a", "sk_test_aaa")
	h.bindStripe(t, app.ID.String(), conn)

	body := []byte(`{"id":"evt_sig","type":"checkout.session.completed","livemode":false,"data":{"object":{}}}`)
	ts := time.Now().UTC()

	rr := h.postWebhook(t, conn, body, signStripe(t, body, "wrong", ts))
	if rr.Code != 400 {
		t.Fatalf("wrong secret want 400, got %d", rr.Code)
	}
	tampered := append([]byte{}, body...)
	tampered[2] = 'X'
	rr = h.postWebhook(t, conn, tampered, signStripe(t, body, whsec, ts))
	if rr.Code != 400 {
		t.Fatalf("tampered want 400, got %d", rr.Code)
	}
	rr = h.postWebhook(t, conn, body, signStripe(t, body, whsec, ts.Add(-10*time.Minute)))
	if rr.Code != 400 {
		t.Fatalf("stale want 400, got %d", rr.Code)
	}
	rr = h.postWebhook(t, conn, body, signStripe(t, body, whsec, ts))
	if rr.Code != 200 {
		t.Fatalf("ok want 200, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestStripeWebhookReplayAndFulfil(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "acme")
	conn := h.createStripeConn(t, "stripe-a", "sk_test_aaa")
	h.bindStripe(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "u@ex.com")

	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack100", Name: "100", Credits: 100, PriceAmount: 499, Currency: "usd",
		StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutBody, _ := json.Marshal(map[string]any{
		"product_id":  prod.ID.String(),
		"success_url": "https://acme.com/thanks", "cancel_url": "https://acme.com/pricing",
	})
	req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(checkoutBody))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("checkout: %d %s", rr.Code, rr.Body.String())
	}
	var co map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &co)
	sessionID := co["session_id"].(string)
	purchaseID := co["purchase_id"].(string)

	evt := map[string]any{
		"id": "evt_pay_1", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 499, "currency": "usd",
			"payment_intent": "pi_1", "metadata": map[string]string{
				"app_id": app.ID.String(), "purchase_id": purchaseID, "user_id": userID.String(),
			},
		}},
	}
	body, _ := json.Marshal(evt)
	sig := signStripe(t, body, whsec, time.Now().UTC())

	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = h.postWebhook(t, conn, body, sig).Code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("replay[%d]=%d", i, c)
		}
	}
	bal, err := h.credits.GetBalance(context.Background(), app.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Balance != 100 {
		t.Fatalf("balance want 100, got %d", bal.Balance)
	}

	// wrong app_id in metadata → ignored, no double grant
	evt2 := map[string]any{
		"id": "evt_wrong_app", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 499, "currency": "usd",
			"payment_intent": "pi_1", "metadata": map[string]string{"app_id": uuid.Must(uuid.NewV7()).String()},
		}},
	}
	body2, _ := json.Marshal(evt2)
	rr = h.postWebhook(t, conn, body2, signStripe(t, body2, whsec, time.Now().UTC()))
	if rr.Code != 200 {
		t.Fatalf("wrong app: %d", rr.Code)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 100 {
		t.Fatalf("balance changed after ignore: %d", bal.Balance)
	}
}

func TestStripeIsolation(t *testing.T) {
	h := setupStripe(t)
	a := h.createApp(t, "alpha")
	b := h.createApp(t, "beta")
	conn := h.createStripeConn(t, "stripe-a", "sk_test_aaa")
	h.bindStripe(t, a.ID.String(), conn)

	// bind same connection to second app → 409
	appB, _ := h.tSvc.Get(context.Background(), b.ID)
	cfgBody, _ := json.Marshal(map[string]any{
		"settings": appB.Settings,
		"integrations": map[string]any{
			"stripe": map[string]any{"connection_id": conn},
		},
	})
	req := httptest.NewRequest("PUT", "/api/v1/admin/apps/"+b.ID.String()+"/config", bytes.NewReader(cfgBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("second bind want 409, got %d %s", rr.Code, rr.Body.String())
	}

	// duplicate account_id
	body, _ := json.Marshal(map[string]any{
		"provider": "stripe", "name": "dup",
		"settings": map[string]any{"livemode": false},
		"secrets":  map[string]any{"secret_key": "sk_test_bbb", "webhook_secrets": []string{whsec}},
	})
	req = httptest.NewRequest("POST", "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("dup account want 409, got %d %s", rr.Code, rr.Body.String())
	}

	// price invalid when stripe returns 404
	h.priceOK = false
	_, err := h.catalog.Create(context.Background(), a.ID, catalog.CreateInput{
		SKU: "bad", Name: "Bad", Credits: 10, PriceAmount: 100, Currency: "usd",
		StripePriceID: strPtr("price_other"),
	})
	if err == nil {
		t.Fatal("want stripe_price_invalid")
	}
}

func TestStripeRefunds(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "acme")
	conn := h.createStripeConn(t, "stripe-a", "sk_test_aaa")
	h.bindStripe(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "r@ex.com")

	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack100", Name: "100", Credits: 100, PriceAmount: 1000, Currency: "usd",
		StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutBody, _ := json.Marshal(map[string]any{
		"product_id":  prod.ID.String(),
		"success_url": "https://acme.com/t", "cancel_url": "https://acme.com/c",
	})
	req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(checkoutBody))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	var co map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &co)
	sessionID := co["session_id"].(string)
	purchaseID := co["purchase_id"].(string)

	payEvt, _ := json.Marshal(map[string]any{
		"id": "evt_f1", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 1000, "currency": "usd",
			"payment_intent": "pi_ref", "metadata": map[string]string{"app_id": app.ID.String(), "purchase_id": purchaseID},
		}},
	})
	h.postWebhook(t, conn, payEvt, signStripe(t, payEvt, whsec, time.Now().UTC()))

	// partial refund 400/1000 → 40 credits
	ref1, _ := json.Marshal(map[string]any{
		"id": "evt_r1", "type": "charge.refunded", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "ch_1", "payment_intent": "pi_ref", "amount": 1000, "amount_refunded": 400,
		}},
	})
	h.postWebhook(t, conn, ref1, signStripe(t, ref1, whsec, time.Now().UTC()))
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 60 {
		t.Fatalf("after partial want 60, got %d", bal.Balance)
	}

	// second partial to full (1000)
	ref2, _ := json.Marshal(map[string]any{
		"id": "evt_r2", "type": "charge.refunded", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "ch_1", "payment_intent": "pi_ref", "amount": 1000, "amount_refunded": 1000,
		}},
	})
	h.postWebhook(t, conn, ref2, signStripe(t, ref2, whsec, time.Now().UTC()))
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 0 {
		t.Fatalf("after full want 0, got %d", bal.Balance)
	}

	// duplicate refund event
	h.postWebhook(t, conn, ref2, signStripe(t, ref2, whsec, time.Now().UTC()))
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 0 {
		t.Fatalf("dup refund changed balance: %d", bal.Balance)
	}

	// refund after spending → negative
	access2, user2 := h.register(t, app, "s@ex.com")
	prod2, _ := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack50", Name: "50", Credits: 50, PriceAmount: 500, Currency: "usd", StripePriceID: strPtr("price_ok2"),
	})
	checkoutBody, _ = json.Marshal(map[string]any{
		"product_id": prod2.ID.String(), "success_url": "https://acme.com/t", "cancel_url": "https://acme.com/c",
	})
	req = httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(checkoutBody))
	req.Header.Set("Authorization", "Bearer "+access2)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	_ = json.Unmarshal(rr.Body.Bytes(), &co)
	sessionID = co["session_id"].(string)
	purchaseID = co["purchase_id"].(string)
	payEvt, _ = json.Marshal(map[string]any{
		"id": "evt_f2", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 500, "currency": "usd",
			"payment_intent": "pi_spend", "metadata": map[string]string{"app_id": app.ID.String(), "purchase_id": purchaseID},
		}},
	})
	h.postWebhook(t, conn, payEvt, signStripe(t, payEvt, whsec, time.Now().UTC()))
	_, err = h.credits.Consume(context.Background(), app.ID, user2, 30, "idem-spend", "feat", "user:"+user2.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refSpend, _ := json.Marshal(map[string]any{
		"id": "evt_r3", "type": "charge.refunded", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "ch_2", "payment_intent": "pi_spend", "amount": 500, "amount_refunded": 500,
		}},
	})
	h.postWebhook(t, conn, refSpend, signStripe(t, refSpend, whsec, time.Now().UTC()))
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, user2)
	if bal.Balance != -30 {
		t.Fatalf("want -30 after refund+spend, got %d", bal.Balance)
	}
	_, err = h.credits.Consume(context.Background(), app.ID, user2, 1, "idem-402", "feat", "user:"+user2.String(), nil)
	if err == nil {
		t.Fatal("want 402 / insufficient")
	}
}

func strPtr(s string) *string { return &s }
