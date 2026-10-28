package tenant_test

import (
	"context"
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
