package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/server"
)

func TestAdminActivityListsPurchasesAndLedger(t *testing.T) {
	srv, _, database := testServer(t)
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		return activityDo(t, srv, method, path, body)
	}

	rr := do("POST", "/api/v1/admin/apps", map[string]any{"slug": "act", "name": "Act"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create app: %d %s", rr.Code, rr.Body.String())
	}
	var app map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &app)
	appID := uuid.Must(uuid.Parse(app["id"].(string)))

	rr = do("POST", "/api/v1/admin/apps", map[string]any{"slug": "other", "name": "Other"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create other: %d %s", rr.Code, rr.Body.String())
	}
	var other map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &other)
	otherID := uuid.Must(uuid.Parse(other["id"].(string)))

	now := db.Now()
	userID := uuid.Must(uuid.NewV7())
	if err := insertActivityUser(t, database, appID, userID, "buyer@example.com", "ACTCODE1", now); err != nil {
		t.Fatal(err)
	}
	otherUser := uuid.Must(uuid.NewV7())
	if err := insertActivityUser(t, database, otherID, otherUser, "other@example.com", "OTHRCODE", now); err != nil {
		t.Fatal(err)
	}
	prodID := uuid.Must(uuid.NewV7())
	if err := catalog.NewStore(database).Insert(t.Context(), nil, catalog.Product{
		ID: prodID, AppID: appID, SKU: "pack", Name: "Pack", Kind: catalog.KindCreditPack,
		Credits: 100, PriceAmount: 499, Currency: "usd", Active: true, Metadata: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	otherProd := uuid.Must(uuid.NewV7())
	if err := catalog.NewStore(database).Insert(t.Context(), nil, catalog.Product{
		ID: otherProd, AppID: otherID, SKU: "other", Name: "Other", Kind: catalog.KindCreditPack,
		Credits: 10, PriceAmount: 100, Currency: "usd", Active: true, Metadata: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	store := billing.NewStore(database)
	amount := int64(499)
	currency := "usd"
	pref := "cs_act"
	pay := "pi_act"
	if err := store.InsertPurchase(t.Context(), nil, billing.Purchase{
		ID: uuid.Must(uuid.NewV7()), AppID: appID, UserID: userID, ProductID: prodID,
		Provider: billing.ProviderStripe, ConnectionID: "c", ProviderRef: &pref, PaymentRef: &pay,
		Status: billing.StatusCompleted, Credits: 100, Amount: &amount, Currency: &currency,
		Environment: billing.EnvProduction, ProviderData: []byte(`{}`),
		CreatedAt: now, UpdatedAt: now, CompletedAt: db.NullTime{Time: now, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	pendingRef := "cs_pending"
	if err := store.InsertPurchase(t.Context(), nil, billing.Purchase{
		ID: uuid.Must(uuid.NewV7()), AppID: appID, UserID: userID, ProductID: prodID,
		Provider: billing.ProviderApple, ConnectionID: "c", ProviderRef: &pendingRef,
		Status: billing.StatusPending, Credits: 100, Environment: billing.EnvSandbox,
		ProviderData: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	otherRef := "cs_other"
	if err := store.InsertPurchase(t.Context(), nil, billing.Purchase{
		ID: uuid.Must(uuid.NewV7()), AppID: otherID, UserID: otherUser, ProductID: otherProd,
		Provider: billing.ProviderStripe, ConnectionID: "c", ProviderRef: &otherRef,
		Status: billing.StatusCompleted, Credits: 10, Environment: billing.EnvProduction,
		ProviderData: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/purchases", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "buyer@example.com") || !strings.Contains(rr.Body.String(), "pi_act") || strings.Contains(rr.Body.String(), "other@example.com") {
		t.Fatalf("purchases: %d %s", rr.Code, rr.Body.String())
	}
	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/purchases?status=completed&provider=stripe&q=buyer", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pi_act") || strings.Contains(rr.Body.String(), "cs_pending") {
		t.Fatalf("filtered purchases: %d %s", rr.Code, rr.Body.String())
	}
	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/purchases?status=nope", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d %s", rr.Code, rr.Body.String())
	}

	rr = do("POST", "/api/v1/admin/apps/"+appID.String()+"/users/"+userID.String()+"/credits", map[string]any{
		"entry_type": "admin_adjust", "amount": 20, "reason": "seed",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("adjust: %d %s", rr.Code, rr.Body.String())
	}
	creditSvc := credit.NewService(database, credit.NewStore(database))
	if _, err := creditSvc.Consume(t.Context(), appID, userID, 5, "req-1", "chat", "api_key", nil); err != nil {
		t.Fatal(err)
	}

	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/ledger?q=buyer@example.com", nil)
	body := rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "admin_adjust") || !strings.Contains(body, "ai_consume") {
		t.Fatalf("ledger: %d %s", rr.Code, body)
	}
	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/ledger?entry_type=ai_consume&user_id="+userID.String(), nil)
	body = rr.Body.String()
	if rr.Code != http.StatusOK || !strings.Contains(body, "ai_consume") || strings.Contains(body, "admin_adjust") {
		t.Fatalf("consume filter: %d %s", rr.Code, body)
	}
	rr = do("GET", "/api/v1/admin/apps/"+appID.String()+"/ledger?entry_type=not_a_type", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad entry type: %d %s", rr.Code, rr.Body.String())
	}

	userPath := "/api/v1/admin/apps/" + appID.String() + "/users/" + userID.String()
	if rr := do("PATCH", userPath, map[string]any{}); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty patch want 400, got %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("PATCH", userPath, map[string]any{"password": "short"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("short password want 400, got %d %s", rr.Code, rr.Body.String())
	}
	newPW := "new_admin_set_pw_99"
	rr = do("PATCH", userPath, map[string]any{"password": newPW})
	if rr.Code != http.StatusOK {
		t.Fatalf("set password: %d %s", rr.Code, rr.Body.String())
	}
	u, err := auth.NewStore(database).GetByID(t.Context(), nil, appID, userID)
	if err != nil || u.PasswordHash == nil {
		t.Fatalf("get user: %v hash=%v", err, u.PasswordHash)
	}
	if ok, _ := auth.VerifyPassword(newPW, *u.PasswordHash); !ok {
		t.Fatal("VerifyPassword failed after admin password update")
	}
	auditRR := do("GET", "/api/v1/admin/audit-log?action=user.update", nil)
	if auditRR.Code != http.StatusOK || strings.Contains(auditRR.Body.String(), newPW) {
		t.Fatalf("audit leaked password or failed: %d %s", auditRR.Code, auditRR.Body.String())
	}
}

func insertActivityUser(t *testing.T, database *db.DB, appID, userID uuid.UUID, email, code string, now db.Time) error {
	t.Helper()
	ts := time.Time(now).UTC().Format(time.RFC3339Nano)
	_, err := database.ExecContext(t.Context(), database.Rebind(`
		INSERT INTO users (id, app_id, email, display_name, avatar_url, status, attribution, referral_code, created_at, updated_at)
		VALUES (?, ?, ?, '', '', 'active', '{}', ?, ?, ?)`),
		userID.String(), appID.String(), email, code, ts, ts)
	return err
}

func activityDo(t *testing.T, srv *server.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	var req *http.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	return rr
}
