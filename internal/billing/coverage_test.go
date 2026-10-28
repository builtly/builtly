package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/affiliate"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/billing/playstore"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/tenant"
)

func TestBillingStoreListAndMark(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "billcov", "BC", tenant.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	now := db.Now()
	prodID := uuid.Must(uuid.NewV7())
	_ = catalog.NewStore(database).Insert(context.Background(), nil, catalog.Product{
		ID: prodID, AppID: app.ID, SKU: "p", Name: "p", Kind: catalog.KindCreditPack,
		Credits: 10, PriceAmount: 100, Currency: "usd", Active: true,
		Metadata: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	})
	userID := uuid.Must(uuid.NewV7())
	email := "u@ex.com"
	code := "CODE9999"
	_, err = database.ExecContext(context.Background(), database.Rebind(`
		INSERT INTO users (id, app_id, email, display_name, avatar_url, status, attribution, referral_code, created_at, updated_at)
		VALUES (?, ?, ?, '', '', 'active', '{}', ?, ?, ?)`),
		userID.String(), app.ID.String(), email, code,
		time.Time(now).UTC().Format(time.RFC3339Nano), time.Time(now).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	store := billing.NewStore(database)
	pref := "cs_pending"
	pid := uuid.Must(uuid.NewV7())
	if err := store.InsertPurchase(context.Background(), nil, billing.Purchase{
		ID: pid, AppID: app.ID, UserID: userID, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c", ProviderRef: &pref,
		Status: billing.StatusPending, Credits: 10, Environment: billing.EnvProduction,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPurchaseStatus(context.Background(), nil, app.ID, pid, billing.StatusPending, billing.StatusExpired, db.Now()); err != nil {
		t.Fatal(err)
	}
	all, err := store.ListPurchases(context.Background(), nil, app.ID, "", "", 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("list all: %v n=%d", err, len(all))
	}
	byStatus, err := store.ListPurchases(context.Background(), nil, app.ID, billing.StatusExpired, "", 0)
	if err != nil || len(byStatus) != 1 {
		t.Fatalf("by status: %v", err)
	}
	byProv, err := store.ListPurchases(context.Background(), nil, app.ID, "", billing.ProviderStripe, 5)
	if err != nil || len(byProv) != 1 {
		t.Fatalf("by provider: %v", err)
	}
	both, err := store.ListPurchases(context.Background(), nil, app.ID, billing.StatusExpired, billing.ProviderStripe, 5)
	if err != nil || len(both) != 1 {
		t.Fatalf("both: %v", err)
	}
}

func TestBillingServiceAccessors(t *testing.T) {
	h := setupStripe(t)
	if h.billing.Stripe() == nil || h.billing.Store() == nil {
		t.Fatal("accessors nil")
	}
	h.billing.SetAffiliate(affiliate.NewRegistry(nil))
}

func TestVoidedPollerRunCancel(t *testing.T) {
	h := setupIAP(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		billing.NewVoidedPoller(h.billing).Run(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestStripeWebhookLifecycleEvents(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "life")
	conn := h.createStripeConn(t, "stripe-life", "sk_test_life")
	h.bindStripe(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "life@ex.com")

	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack", Name: "P", Credits: 50, PriceAmount: 500, Currency: "usd",
		StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutBody, _ := json.Marshal(map[string]any{
		"product_id": prod.ID.String(), "success_url": "https://life.com/t", "cancel_url": "https://life.com/c",
	})

	doCheckout := func() (sessionID, purchaseID string) {
		t.Helper()
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
		return co["session_id"].(string), co["purchase_id"].(string)
	}

	sessionID, purchaseID := doCheckout()

	unpaid, _ := json.Marshal(map[string]any{
		"id": "evt_unpaid", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "unpaid", "amount_total": 500, "currency": "usd",
			"payment_intent": "pi_u", "metadata": map[string]string{"app_id": app.ID.String(), "purchase_id": purchaseID},
		}},
	})
	if c := h.postWebhook(t, conn, unpaid, signStripe(t, unpaid, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("unpaid: %d", c)
	}

	asyncFail, _ := json.Marshal(map[string]any{
		"id": "evt_async_fail", "type": "checkout.session.async_payment_failed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "metadata": map[string]string{"app_id": app.ID.String(), "purchase_id": purchaseID},
		}},
	})
	if c := h.postWebhook(t, conn, asyncFail, signStripe(t, asyncFail, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("async fail: %d", c)
	}

	sessionID, _ = doCheckout()
	expired, _ := json.Marshal(map[string]any{
		"id": "evt_exp", "type": "checkout.session.expired", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "metadata": map[string]string{"app_id": app.ID.String()},
		}},
	})
	if c := h.postWebhook(t, conn, expired, signStripe(t, expired, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("expired: %d", c)
	}

	sessionID, purchaseID = doCheckout()
	paid, _ := json.Marshal(map[string]any{
		"id": "evt_paid_life", "type": "checkout.session.completed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 500, "currency": "usd",
			"payment_intent": "pi_life", "metadata": map[string]string{
				"app_id": app.ID.String(), "purchase_id": purchaseID, "user_id": userID.String(),
			},
		}},
	})
	h.postWebhook(t, conn, paid, signStripe(t, paid, whsec, time.Now().UTC()))

	disp, _ := json.Marshal(map[string]any{
		"id": "evt_disp", "type": "charge.dispute.created", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "dp_1", "payment_intent": "pi_life", "status": "needs_response",
		}},
	})
	if c := h.postWebhook(t, conn, disp, signStripe(t, disp, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("dispute: %d", c)
	}
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 0 {
		t.Fatalf("after dispute want 0, got %d", bal.Balance)
	}

	won, _ := json.Marshal(map[string]any{
		"id": "evt_won", "type": "charge.dispute.closed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "dp_1", "payment_intent": "pi_life", "status": "won",
		}},
	})
	if c := h.postWebhook(t, conn, won, signStripe(t, won, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("dispute won: %d", c)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 50 {
		t.Fatalf("after won want 50, got %d", bal.Balance)
	}

	lost, _ := json.Marshal(map[string]any{
		"id": "evt_lost", "type": "charge.dispute.closed", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "dp_2", "payment_intent": "pi_life", "status": "lost",
		}},
	})
	h.postWebhook(t, conn, lost, signStripe(t, lost, whsec, time.Now().UTC()))

	unknown, _ := json.Marshal(map[string]any{
		"id": "evt_unk", "type": "customer.created", "livemode": false,
		"data": map[string]any{"object": map[string]any{}},
	})
	if c := h.postWebhook(t, conn, unknown, signStripe(t, unknown, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("unknown: %d", c)
	}

	miss, _ := json.Marshal(map[string]any{
		"id": "evt_miss", "type": "charge.refunded", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": "ch_x", "payment_intent": "pi_missing", "amount": 100, "amount_refunded": 100,
		}},
	})
	if c := h.postWebhook(t, conn, miss, signStripe(t, miss, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("miss refund: %d", c)
	}
}

func TestCheckoutValidationAndErrors(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "chk")
	conn := h.createStripeConn(t, "stripe-chk", "sk_test_chk")
	h.bindStripe(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "chk@ex.com")
	_ = userID

	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack", Name: "P", Credits: 10, PriceAmount: 100, Currency: "usd",
		StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}

	badBodies := []map[string]any{
		{"product_id": prod.ID.String(), "success_url": "ftp://chk.com/t", "cancel_url": "https://chk.com/c"},
		{"product_id": prod.ID.String(), "success_url": "https://evil.com/t", "cancel_url": "https://chk.com/c"},
		{"product_id": prod.ID.String(), "success_url": "http://example.com/t", "cancel_url": "https://chk.com/c"},
		{"product_id": prod.ID.String(), "success_url": "not-a-url", "cancel_url": "https://chk.com/c"},
	}
	for i, b := range badBodies {
		body, _ := json.Marshal(b)
		req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("X-App-ID", app.ID.String())
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.srv.Router().ServeHTTP(rr, req)
		if rr.Code == 201 {
			t.Fatalf("case %d want validation error, got 201", i)
		}
	}

	// localhost http allowed in dev
	okBody, _ := json.Marshal(map[string]any{
		"product_id": prod.ID.String(), "success_url": "http://localhost:3000/t", "cancel_url": "http://localhost:3000/c",
		"affiliate_ref": "aff_1",
	})
	req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(okBody))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("localhost checkout: %d %s", rr.Code, rr.Body.String())
	}

	// inactive / no price product
	inactive, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "dead", Name: "D", Credits: 1, PriceAmount: 1, Currency: "usd",
		StripePriceID: strPtr("price_ok2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := h.catalog.Patch(context.Background(), app.ID, inactive.ID, catalog.PatchInput{Active: &off}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"product_id": inactive.ID.String(), "success_url": "https://chk.com/t", "cancel_url": "https://chk.com/c",
	})
	req = httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 201 {
		t.Fatal("inactive product accepted")
	}

	h.billing.SetAffiliate(affiliate.NewRegistry(nil))
	okBody, _ = json.Marshal(map[string]any{
		"product_id": prod.ID.String(), "success_url": "https://chk.com/t", "cancel_url": "https://chk.com/c",
		"affiliate_ref": "aff_2",
	})
	req = httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(okBody))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("affiliate checkout: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAppleWebhookEdgePaths(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "apledge")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader([]byte(`{`)))
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("bad json: %d", rr.Code)
	}

	req = httptest.NewRequest("POST", "/api/v1/webhooks/apple/no-such-conn", bytes.NewReader([]byte(`{"signedPayload":"x.y.z"}`)))
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 404 && rr.Code != 400 {
		t.Fatalf("unknown conn: %d", rr.Code)
	}

	notif := h.appleChain.sign(t, map[string]any{
		"notificationType": "TEST", "notificationUUID": "notif-ignore",
		"data": map[string]any{
			"bundleId": "com.example.app", "environment": "Production",
			"signedTransactionInfo": "not.a.tx",
		},
	})
	body, _ := json.Marshal(map[string]string{"signedPayload": notif})
	req = httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("ignored type: %d %s", rr.Code, rr.Body.String())
	}

	// wrong bundle ignored
	wrong := h.appleChain.sign(t, map[string]any{
		"notificationType": "REFUND", "notificationUUID": "notif-wrong-bundle",
		"data": map[string]any{
			"bundleId": "com.other.app", "environment": "Production",
			"signedTransactionInfo": h.appleChain.sign(t, map[string]any{
				"transactionId": "tx_x", "bundleId": "com.other.app", "productId": "p",
				"type": "Consumable", "environment": "Production",
			}),
		},
	})
	body, _ = json.Marshal(map[string]string{"signedPayload": wrong})
	req = httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+conn, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("wrong bundle: %d %s", rr.Code, rr.Body.String())
	}
}

func TestIAPValidationFailures(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "iapval")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)
	access, userID := h.register(t, app, "iapval@ex.com")

	appleID := "credits_val"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pval", Name: "V", Credits: 10, PriceAmount: 0, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// wrong environment / product mismatch covered via putAppleTx then mutate
	txID := "tx_val_1"
	h.putAppleTx(t, txID, "wrong_product", "Production", userID)
	rr := h.verifyApple(t, access, app.ID, txID)
	if rr.Code == 200 {
		t.Fatal("want product mismatch failure")
	}

	gConn := h.createGoogleConn(t)
	h.bind(t, app.ID.String(), "google_play", gConn)
	gID := "credits_g"
	_, err = h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pg", Name: "G", Credits: 5, PriceAmount: 0, Currency: "usd",
		GoogleProductID: &gID,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.googleMu.Lock()
	h.googlePurch["tok_bad"] = playstore.ProductPurchase{
		PurchaseState: 1, ConsumptionState: 0, OrderID: "GPA.bad",
	}
	h.googleMu.Unlock()
	rr = h.verifyGoogle(t, access, app.ID, gID, "tok_bad")
	if rr.Code == 200 {
		t.Fatal("want google validation failure")
	}
}

func TestStripeWebhookUnboundConnection(t *testing.T) {
	h := setupStripe(t)
	conn := h.createStripeConn(t, "unbound", "sk_test_unbound")
	// no bind
	body, _ := json.Marshal(map[string]any{
		"id": "evt_unbound", "type": "customer.created", "livemode": false,
		"data": map[string]any{"object": map[string]any{}},
	})
	rr := h.postWebhook(t, conn, body, signStripe(t, body, whsec, time.Now().UTC()))
	if rr.Code != 200 {
		t.Fatalf("unbound: %d", rr.Code)
	}
}

func TestIAPAppleValidateVariants(t *testing.T) {
	h := setupIAP(t)
	app := h.createApp(t, "iapvar")
	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)
	access, userID := h.register(t, app, "iapvar@ex.com")
	appleID := "credits_var"
	_, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pvar", Name: "V", Credits: 10, PriceAmount: 0, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}

	put := func(txID string, claims map[string]any) {
		t.Helper()
		jws := h.appleChain.sign(t, claims)
		h.txMu.Lock()
		h.appleTx[txID] = jws
		h.txMu.Unlock()
	}

	put("tx_nc", map[string]any{
		"transactionId": "tx_nc", "bundleId": "com.example.app", "productId": appleID,
		"type": "Non-Consumable", "environment": "Production", "appAccountToken": userID.String(),
	})
	if h.verifyApple(t, access, app.ID, "tx_nc").Code == 200 {
		t.Fatal("non-consumable")
	}

	rev := float64(time.Now().UnixMilli())
	put("tx_rev", map[string]any{
		"transactionId": "tx_rev", "bundleId": "com.example.app", "productId": appleID,
		"type": "Consumable", "environment": "Production", "appAccountToken": userID.String(),
		"revocationDate": rev,
	})
	if h.verifyApple(t, access, app.ID, "tx_rev").Code == 200 {
		t.Fatal("revoked")
	}

	put("tx_acct", map[string]any{
		"transactionId": "tx_acct", "bundleId": "com.example.app", "productId": appleID,
		"type": "Consumable", "environment": "Production", "appAccountToken": uuid.Must(uuid.NewV7()).String(),
	})
	if h.verifyApple(t, access, app.ID, "tx_acct").Code == 200 {
		t.Fatal("account mismatch")
	}

	// google account mismatch + sandbox block
	app2, err := h.tSvc.Create(context.Background(), "nosand", "NS", tenant.Settings{
		AllowedOrigins:  []string{"https://nosand.com"},
		AllowIAPSandbox: false,
		Auth:            tenant.AuthSettings{PasswordEnabled: true},
		Email:           tenant.EmailSettings{FromAddress: "n@nosand.com", ActionBaseURL: "https://nosand.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gConn := h.createGoogleConn(t)
	h.bind(t, app2.ID.String(), "google_play", gConn)
	access2, user2 := h.register(t, app2, "ns@ex.com")
	gID := "credits_ns"
	_, err = h.catalog.Create(context.Background(), app2.ID, catalog.CreateInput{
		SKU: "pns", Name: "NS", Credits: 5, PriceAmount: 0, Currency: "usd",
		GoogleProductID: &gID,
	})
	if err != nil {
		t.Fatal(err)
	}
	pt := 0
	h.googleMu.Lock()
	h.googlePurch["tok_sand"] = playstore.ProductPurchase{
		PurchaseState: 0, ConsumptionState: 0, OrderID: "GPA.sand", PurchaseType: &pt,
		ObfuscatedExternalAccountID: user2.String(),
	}
	h.googleMu.Unlock()
	if h.verifyGoogle(t, access2, app2.ID, gID, "tok_sand").Code == 200 {
		t.Fatal("sandbox should be blocked")
	}
	h.googleMu.Lock()
	h.googlePurch["tok_mis"] = playstore.ProductPurchase{
		PurchaseState: 0, ConsumptionState: 0, OrderID: "GPA.mis",
		ObfuscatedExternalAccountID: uuid.Must(uuid.NewV7()).String(),
	}
	h.googleMu.Unlock()
	if h.verifyGoogle(t, access2, app2.ID, gID, "tok_mis").Code == 200 {
		t.Fatal("account mismatch google")
	}

	// apple sandbox notif when sandbox disabled
	appleConn := h.createAppleConn(t)
	h.bind(t, app2.ID.String(), "apple_iap", appleConn)
	notif := h.appleChain.sign(t, map[string]any{
		"notificationType": "REFUND", "notificationUUID": "notif-sand-block",
		"data": map[string]any{
			"bundleId": "com.example.app", "environment": "Sandbox",
			"signedTransactionInfo": h.appleChain.sign(t, map[string]any{
				"transactionId": "tx_sb", "bundleId": "com.example.app", "productId": "x",
				"type": "Consumable", "environment": "Sandbox",
			}),
		},
	})
	body, _ := json.Marshal(map[string]string{"signedPayload": notif})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/apple/"+appleConn, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("sandbox webhook: %d", rr.Code)
	}
}

func TestCheckoutUpstreamFailureAndBadJSON(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "upfail")
	conn := h.createStripeConn(t, "stripe-up", "sk_test_up")
	h.bindStripe(t, app.ID.String(), conn)
	access, _ := h.register(t, app, "up@ex.com")
	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "p", Name: "P", Credits: 1, PriceAmount: 100, Currency: "usd", StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Break checkout by pointing stripe client at closed server
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", 500)
	}))
	dead.Close()
	h.billing.Stripe().BaseURL = dead.URL

	body, _ := json.Marshal(map[string]any{
		"product_id": prod.ID.String(), "success_url": "https://upfail.com/t", "cancel_url": "https://upfail.com/c",
	})
	req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 201 {
		t.Fatal("want upstream failure")
	}

	req = httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader([]byte(`{`)))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 201 {
		t.Fatal("bad json checkout")
	}
	req = httptest.NewRequest("POST", "/api/v1/billing/verify-iap", bytes.NewReader([]byte(`{`)))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatal("bad json iap")
	}
}

func TestBillingMoreErrorBranches(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "errbr")
	access, _ := h.register(t, app, "errbr@ex.com")

	// checkout without stripe binding
	body, _ := json.Marshal(map[string]any{
		"product_id":  uuid.Must(uuid.NewV7()).String(),
		"success_url": "https://errbr.com/t", "cancel_url": "https://errbr.com/c",
	})
	req := httptest.NewRequest("POST", "/api/v1/checkout/stripe", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 201 {
		t.Fatal("unbound checkout")
	}

	conn := h.createStripeConn(t, "stripe-err", "sk_test_err")
	h.bindStripe(t, app.ID.String(), conn)
	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pe", Name: "E", Credits: 1, PriceAmount: 100, Currency: "usd", StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = prod

	// livemode mismatch
	body, _ = json.Marshal(map[string]any{
		"id": "evt_live", "type": "customer.created", "livemode": true,
		"data": map[string]any{"object": map[string]any{}},
	})
	rr = h.postWebhook(t, conn, body, signStripe(t, body, whsec, time.Now().UTC()))
	if rr.Code != 400 {
		t.Fatalf("livemode want 400, got %d", rr.Code)
	}

	// unknown connection
	rr = h.postWebhook(t, "missing-conn", body, signStripe(t, body, whsec, time.Now().UTC()))
	if rr.Code != 404 {
		t.Fatalf("missing conn: %d", rr.Code)
	}

	// IAP without binding
	iapBody, _ := json.Marshal(map[string]any{"platform": "apple", "transaction_id": "x"})
	req = httptest.NewRequest("POST", "/api/v1/billing/verify-iap", bytes.NewReader(iapBody))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatal("iap unbound")
	}
}

func TestStripeAsyncPaidAndBadEventJSON(t *testing.T) {
	h := setupStripe(t)
	app := h.createApp(t, "async")
	conn := h.createStripeConn(t, "stripe-async", "sk_test_async")
	h.bindStripe(t, app.ID.String(), conn)
	access, userID := h.register(t, app, "async@ex.com")
	prod, err := h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pa", Name: "A", Credits: 20, PriceAmount: 200, Currency: "usd", StripePriceID: strPtr("price_ok"),
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutBody, _ := json.Marshal(map[string]any{
		"product_id": prod.ID.String(), "success_url": "https://async.com/t", "cancel_url": "https://async.com/c",
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

	evt, _ := json.Marshal(map[string]any{
		"id": "evt_async_ok", "type": "checkout.session.async_payment_succeeded", "livemode": false,
		"data": map[string]any{"object": map[string]any{
			"id": sessionID, "payment_status": "paid", "amount_total": 200, "currency": "usd",
			"payment_intent": "pi_async", "metadata": map[string]string{
				"app_id": app.ID.String(), "purchase_id": purchaseID, "user_id": userID.String(),
			},
		}},
	})
	if c := h.postWebhook(t, conn, evt, signStripe(t, evt, whsec, time.Now().UTC())).Code; c != 200 {
		t.Fatalf("async paid: %d", c)
	}
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, userID)
	if bal.Balance != 20 {
		t.Fatalf("balance %d", bal.Balance)
	}

	// valid signature but broken JSON body shape for event id empty still parses
	bad, _ := json.Marshal(map[string]any{"id": "evt_badjson", "type": "x", "livemode": false, "data": "not-object"})
	// data as string may still unmarshal into stripeEvent with RawMessage
	h.postWebhook(t, conn, bad, signStripe(t, bad, whsec, time.Now().UTC()))

	// completely invalid json with valid-looking sig over that body
	raw := []byte(`not-json`)
	rr = h.postWebhook(t, conn, raw, signStripe(t, raw, whsec, time.Now().UTC()))
	if rr.Code != 400 {
		t.Fatalf("bad json event: %d", rr.Code)
	}
}
