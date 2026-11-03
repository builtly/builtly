package tenant_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/tenant"
)

func TestAppCRUDAndMiddleware(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	store := tenant.NewStore(database)
	svc := tenant.NewService(database, store)
	ctx := context.Background()

	app, err := svc.Create(ctx, "acme", "Acme", tenant.Settings{
		AllowedOrigins: []string{"https://acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.Settings.Auth.PasswordEnabled != true {
		t.Fatal("defaults not merged")
	}

	_, err = svc.Create(ctx, "acme", "Dup", tenant.Settings{})
	if err != tenant.ErrSlugTaken {
		t.Fatalf("want slug_taken, got %v", err)
	}

	got, err := svc.Get(ctx, app.ID)
	if err != nil || got.Slug != "acme" {
		t.Fatalf("%v %+v", err, got)
	}

	mw := tenant.NewMiddleware(svc, 600)
	h := mw.Tenant(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := tenant.FromContext(r.Context())
		if !ok || a.ID != app.ID {
			t.Errorf("missing app in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Origin", "https://acme.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req2.Header.Set("X-App-ID", app.ID.String())
	req2.Header.Set("Origin", "https://evil.com")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rr2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Fatalf("want 400 missing app, got %d", rr3.Code)
	}

	key, raw, err := svc.CreateAPIKey(ctx, app.ID, "proxy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || key.Prefix == "" {
		t.Fatal("expected raw key once")
	}
	looked, err := svc.LookupAPIKey(ctx, raw)
	if err != nil || looked.ID != key.ID {
		t.Fatalf("%v %+v", err, looked)
	}
	if err := svc.RevokeAPIKey(ctx, app.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LookupAPIKey(ctx, raw); err == nil {
		t.Fatal("expected revoked")
	}

	_, err = svc.Get(ctx, uuid.Must(uuid.NewV7()))
	if err != tenant.ErrAppNotFound {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestSettingsGuestLoginValidationAndMatching(t *testing.T) {
	// 1. GuestLoginEnabled true without GuestEmailDomain should fail validation
	st := tenant.DefaultSettings()
	st.Auth.GuestLoginEnabled = true
	st.Auth.GuestEmailDomain = ""
	if err := st.Validate(); err == nil {
		t.Fatal("expected error when guest_login_enabled is true but guest_email_domain is empty")
	}

	// 2. GuestLoginEnabled true with invalid domain
	st.Auth.GuestEmailDomain = "invalid domain.com"
	if err := st.Validate(); err == nil {
		t.Fatal("expected error with invalid guest_email_domain")
	}

	// 3. GuestLoginEnabled true with valid domain
	st.Auth.GuestEmailDomain = "device.example.com"
	if err := st.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// 4. IsGuestEmail when GuestLoginEnabled is false
	st.Auth.GuestLoginEnabled = false
	if st.IsGuestEmail("guest_dev123@device.example.com") {
		t.Fatal("IsGuestEmail should return false when GuestLoginEnabled is false")
	}

	// 5. IsGuestEmail when GuestLoginEnabled is true
	st.Auth.GuestLoginEnabled = true
	if !st.IsGuestEmail("guest_dev123@device.example.com") {
		t.Fatal("IsGuestEmail should return true for valid guest email")
	}
	if st.IsGuestEmail("regular@device.example.com") {
		t.Fatal("IsGuestEmail should return false for regular email prefix")
	}
	if st.IsGuestEmail("guest_@device.example.com") {
		t.Fatal("IsGuestEmail should return false for empty guest id")
	}
}

func TestIAPSandboxEmails(t *testing.T) {
	st := tenant.DefaultSettings()
	st.IAPSandboxEmails = []string{"not-an-email"}
	if err := st.Validate(); err == nil {
		t.Fatal("expected invalid iap_sandbox_emails")
	}

	raw, err := json.Marshal(map[string]any{
		"iap_sandbox_emails": []string{" Reviewer@Example.com ", "reviewer@example.com", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := tenant.ParseSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.IAPSandboxEmails) != 1 || parsed.IAPSandboxEmails[0] != "reviewer@example.com" {
		t.Fatalf("normalized: %#v", parsed.IAPSandboxEmails)
	}
	if !parsed.SandboxEmailAllowed("Reviewer@Example.com") {
		t.Fatal("allowlist should match case-insensitively")
	}
	if parsed.SandboxEmailAllowed("other@example.com") {
		t.Fatal("unlisted email should be denied")
	}
}
