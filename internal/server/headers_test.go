package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPISecurityHeaders(t *testing.T) {
	srv := adminUIServer(t, false)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	}
	for k, want := range checks {
		if got := rr.Header().Get(k); got != want {
			t.Fatalf("%s=%q want %q", k, got, want)
		}
	}
	if rr.Header().Get("Permissions-Policy") == "" {
		t.Fatal("missing Permissions-Policy")
	}
}
