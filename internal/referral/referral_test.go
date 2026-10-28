package referral_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/server"
	"github.com/builtly/builtly/internal/tenant"
)

const refAdminToken = "test-admin-token-at-least-32-chars!!"

type refHarness struct {
	srv         *server.Server
	db          *db.DB
	tSvc        *tenant.Service
	authSvc     *auth.Service
	userStore   *auth.Store
	credits     *credit.Service
	referralSvc *referral.Service
	billStore   *billing.Store
	catStore    *catalog.Store
}

func setupRef(t *testing.T) *refHarness {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{4}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	jwtKey := bytes.Repeat([]byte{8}, 32)
	cfg := &config.Config{
		Env: "development", PublicURL: "https://api.example.com",
		AdminEnabled: true, AdminToken: refAdminToken,
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
	billStore := billing.NewStore(database)
	billSvc := billing.NewService(database, billStore, catSvc, creditSvc, pub, resolver, iSvc, iStore, box, userStore, tSvc, stripeClient, true)

	refSvc := referral.NewService(database, referral.NewStore(database), creditSvc, pub,
		auth.ReferralUsers{Store: userStore}, billing.ReferralPurchases{Store: billStore}, tSvc)
	authSvc.SetReferrals(refSvc)
	oreg.SetHandler(outbox.SubReferral, referral.NewSubscriber(refSvc).Handle)

	adminH := admin.NewHandler(database, tSvc, iSvc, resolver, admin.NewAuditStore(database), tStore, iStore, tMW, userStore, creditSvc, billing.NewStore(database), outboxStore, box, catSvc, stripeClient, refSvc)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(cfg, database, log, server.Deps{
		Tenant: tSvc, TenantMW: tMW, Secrets: box, AdminAuth: admin.NewAuth(cfg), Admin: adminH,
		AuthMW: auth.NewMiddleware(tokens, tSvc), Credits: creditSvc,
		CreditUser: credit.NewUserHandler(creditSvc), AI: ai.NewHandler(creditSvc, userStore),
		Auth: auth.NewHandler(authSvc, nil), Catalog: catalog.NewHandler(catSvc), Billing: billing.NewHandler(billSvc),
		Referral: referral.NewHandler(refSvc),
	})
	return &refHarness{
		srv: srv, db: database, tSvc: tSvc, authSvc: authSvc, userStore: userStore, credits: creditSvc,
		referralSvc: refSvc, billStore: billStore, catStore: catalog.NewStore(database),
	}
}

func (h *refHarness) createProduct(t *testing.T, appID uuid.UUID, sku string, credits int64) uuid.UUID {
	t.Helper()
	now := db.Now()
	id := uuid.Must(uuid.NewV7())
	p := catalog.Product{
		ID: id, AppID: appID, SKU: sku, Name: sku, Kind: catalog.KindCreditPack,
		Credits: credits, PriceAmount: credits * 10, Currency: "usd",
		Active: true, Metadata: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}
	if err := h.catStore.Insert(context.Background(), nil, p); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *refHarness) createApp(t *testing.T, slug string, enabled bool) tenant.App {
	t.Helper()
	s := tenant.DefaultSettings()
	s.AllowedOrigins = []string{"https://" + slug + ".com"}
	s.Auth.PasswordEnabled = true
	s.Email = tenant.EmailSettings{FromAddress: "n@" + slug + ".com", ActionBaseURL: "https://" + slug + ".com"}
	s.Referral.Enabled = enabled
	s.Referral.Trigger = "first_purchase"
	s.Referral.RewardType = "fixed"
	s.Referral.RewardValue = 50
	s.Referral.RefereeBonusCredits = 10
	s.Referral.ApplyWindowDays = 7
	app, err := h.tSvc.Create(context.Background(), slug, slug, s)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (h *refHarness) register(t *testing.T, app tenant.App, email, refCode string) (uuid.UUID, auth.TokenResponse) {
	t.Helper()
	r, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: email, Password: "password123", ReferralCode: refCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r.User.ID, r
}

func (h *refHarness) codeOf(t *testing.T, appID, userID uuid.UUID) string {
	t.Helper()
	u, err := h.userStore.GetByID(context.Background(), nil, appID, userID)
	if err != nil || u.ReferralCode == nil {
		t.Fatal(err)
	}
	return *u.ReferralCode
}

func TestReferralApplyRules(t *testing.T) {
	h := setupRef(t)
	app := h.createApp(t, "acme", true)
	refID, _ := h.register(t, app, "ref@ex.com", "")
	code := h.codeOf(t, app.ID, refID)

	accessSelf, selfResp := func() (string, auth.TokenResponse) {
		id, r := h.register(t, app, "self2@ex.com", "")
		_ = id
		return r.Tokens.AccessToken, r
	}()
	own := h.codeOf(t, app.ID, selfResp.User.ID)
	body, _ := json.Marshal(map[string]string{"code": own})
	req := httptest.NewRequest("POST", "/api/v1/user/referral/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+accessSelf)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Fatalf("self want 422, got %d %s", rr.Code, rr.Body.String())
	}

	accessN, _ := h.register(t, app, "n@ex.com", "")
	_ = accessN
	loginN, _ := h.authSvc.Login(context.Background(), app, auth.LoginInput{Email: "n@ex.com", Password: "password123"})
	body, _ = json.Marshal(map[string]string{"code": "NOPECODE"})
	req = httptest.NewRequest("POST", "/api/v1/user/referral/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+loginN.Tokens.AccessToken)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Fatalf("invalid want 422, got %d", rr.Code)
	}

	n2, n2Resp := h.register(t, app, "n2@ex.com", code)
	if n2Resp.Referral == nil || !n2Resp.Referral.Applied {
		t.Fatalf("signup apply: %+v", n2Resp.Referral)
	}
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, n2)
	if bal.Balance != 10 {
		t.Fatalf("referee bonus want 10, got %d", bal.Balance)
	}

	login2, err := h.authSvc.Login(context.Background(), app, auth.LoginInput{Email: "n2@ex.com", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]string{"code": code})
	req = httptest.NewRequest("POST", "/api/v1/user/referral/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+login2.Tokens.AccessToken)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("already applied want 409, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestReferralDisabledAtSignup(t *testing.T) {
	h := setupRef(t)
	app := h.createApp(t, "off", false)
	refID, _ := h.register(t, app, "r@ex.com", "")
	code := h.codeOf(t, app.ID, refID)
	_, resp := h.register(t, app, "n@ex.com", code)
	if resp.Referral == nil || resp.Referral.Applied || resp.Referral.Reason != "referral_disabled" {
		t.Fatalf("want soft disabled, got %+v", resp.Referral)
	}
}

func TestReferralRewardsAndRefund(t *testing.T) {
	h := setupRef(t)
	app := h.createApp(t, "pay", true)
	refID, _ := h.register(t, app, "ref@ex.com", "")
	code := h.codeOf(t, app.ID, refID)
	refereeID, _ := h.register(t, app, "ee@ex.com", code)

	now := time.Now().UTC()
	purchaseID := uuid.Must(uuid.NewV7())
	prodID := h.createProduct(t, app.ID, "pack100", 100)
	pref := "cs_test_ref"
	p := billing.Purchase{
		ID: purchaseID, AppID: app.ID, UserID: refereeID, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c1", ProviderRef: &pref,
		Status: billing.StatusCompleted, Credits: 100, Environment: billing.EnvProduction,
		CreatedAt: db.Time(now), UpdatedAt: db.Time(now),
		CompletedAt: db.NullTime{Time: db.Time(now), Valid: true},
	}
	if err := h.billStore.InsertPurchase(context.Background(), nil, p); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"purchase_id": purchaseID.String()})
	appCopy := app.ID
	sub := referral.NewSubscriber(h.referralSvc)
	row := outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseCompleted}
	if err := sub.Handle(context.Background(), row, payload); err != nil {
		t.Fatal(err)
	}
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 50 {
		t.Fatalf("referrer reward want 50, got %d", bal.Balance)
	}
	if err := sub.Handle(context.Background(), row, payload); err != nil {
		t.Fatal(err)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 50 {
		t.Fatalf("redelivery changed balance: %d", bal.Balance)
	}

	p2id := uuid.Must(uuid.NewV7())
	pref2 := "cs_2"
	if err := h.billStore.InsertPurchase(context.Background(), nil, billing.Purchase{
		ID: p2id, AppID: app.ID, UserID: refereeID, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c1", ProviderRef: &pref2,
		Status: billing.StatusCompleted, Credits: 100, Environment: billing.EnvProduction,
		CreatedAt: db.Time(now), UpdatedAt: db.Time(now),
		CompletedAt: db.NullTime{Time: db.Time(now), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	payload2, _ := json.Marshal(map[string]any{"purchase_id": p2id.String()})
	if err := sub.Handle(context.Background(), outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseCompleted}, payload2); err != nil {
		t.Fatal(err)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 50 {
		t.Fatalf("second purchase rewarded: %d", bal.Balance)
	}

	ref2, _ := h.register(t, app, "ref2@ex.com", "")
	code2 := h.codeOf(t, app.ID, ref2)
	ee2, _ := h.register(t, app, "ee2@ex.com", code2)
	psID := uuid.Must(uuid.NewV7())
	prefs := "cs_sand"
	if err := h.billStore.InsertPurchase(context.Background(), nil, billing.Purchase{
		ID: psID, AppID: app.ID, UserID: ee2, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c1", ProviderRef: &prefs,
		Status: billing.StatusCompleted, Credits: 100, Environment: billing.EnvSandbox,
		CreatedAt: db.Time(now), UpdatedAt: db.Time(now),
		CompletedAt: db.NullTime{Time: db.Time(now), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	payloadS, _ := json.Marshal(map[string]any{"purchase_id": psID.String()})
	if err := sub.Handle(context.Background(), outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseCompleted}, payloadS); err != nil {
		t.Fatal(err)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, ref2)
	if bal.Balance != 0 {
		t.Fatalf("sandbox rewarded: %d", bal.Balance)
	}

	if err := h.billStore.SetRefundedCredits(context.Background(), nil, app.ID, purchaseID, 40, billing.StatusPartialRefunded, db.Time(now)); err != nil {
		t.Fatal(err)
	}
	refPayload, _ := json.Marshal(map[string]any{"purchase_id": purchaseID.String()})
	if err := sub.Handle(context.Background(), outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseRefunded}, refPayload); err != nil {
		t.Fatal(err)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 30 {
		t.Fatalf("after partial refund want 30, got %d", bal.Balance)
	}
	if err := h.billStore.SetRefundedCredits(context.Background(), nil, app.ID, purchaseID, 100, billing.StatusRefunded, db.Time(now)); err != nil {
		t.Fatal(err)
	}
	if err := sub.Handle(context.Background(), outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseRefunded}, refPayload); err != nil {
		t.Fatal(err)
	}
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 0 {
		t.Fatalf("after full refund want 0, got %d", bal.Balance)
	}
}

func TestReferralEveryPurchaseAndCap(t *testing.T) {
	h := setupRef(t)
	s := tenant.DefaultSettings()
	s.AllowedOrigins = []string{"https://beta.com"}
	s.Auth.PasswordEnabled = true
	s.Email = tenant.EmailSettings{FromAddress: "n@beta.com", ActionBaseURL: "https://beta.com"}
	s.Referral.Enabled = true
	s.Referral.Trigger = "every_purchase"
	s.Referral.RewardType = "percent"
	s.Referral.RewardValue = 10
	s.Referral.MaxRewardedReferees = 1
	s.Referral.ApplyWindowDays = 30
	app, err := h.tSvc.Create(context.Background(), "beta", "Beta", s)
	if err != nil {
		t.Fatal(err)
	}
	refID, _ := h.register(t, app, "r@b.com", "")
	code := h.codeOf(t, app.ID, refID)
	ee1, _ := h.register(t, app, "e1@b.com", code)
	ee2, _ := h.register(t, app, "e2@b.com", code)

	sub := referral.NewSubscriber(h.referralSvc)
	appCopy := app.ID
	now := time.Now().UTC()
	prodID := h.createProduct(t, app.ID, "pack100", 100)

	grant := func(user uuid.UUID, credits int64) {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		pref := id.String()
		if err := h.billStore.InsertPurchase(context.Background(), nil, billing.Purchase{
			ID: id, AppID: app.ID, UserID: user, ProductID: prodID,
			Provider: billing.ProviderStripe, ConnectionID: "c", ProviderRef: &pref,
			Status: billing.StatusCompleted, Credits: credits, Environment: billing.EnvProduction,
			CreatedAt: db.Time(now), UpdatedAt: db.Time(now),
			CompletedAt: db.NullTime{Time: db.Time(now), Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"purchase_id": id.String()})
		if err := sub.Handle(context.Background(), outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &appCopy, EventType: outbox.EventPurchaseCompleted}, payload); err != nil {
			t.Fatal(err)
		}
	}

	grant(ee1, 100)
	grant(ee1, 100)
	bal, _ := h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 20 {
		t.Fatalf("every_purchase want 20, got %d", bal.Balance)
	}
	grant(ee2, 100)
	bal, _ = h.credits.GetBalance(context.Background(), app.ID, refID)
	if bal.Balance != 20 {
		t.Fatalf("cap should block, got %d", bal.Balance)
	}
}
