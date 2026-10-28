package admin_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/builtly/builtly/internal/platform/logx"
)

// TestSecretsNeverAppearInCapturedLogs covers architecture test 10 (log capture).
func TestSecretsNeverAppearInCapturedLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv, _, _ := testServer(t)
	secret := "re_super_secret_value_do_not_leak"
	body, _ := json.Marshal(map[string]any{
		"provider": "resend", "name": "Mail",
		"settings": map[string]any{},
		"secrets":  map[string]any{"api_key": secret},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/connections", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create conn: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("API response leaked secret")
	}

	// Explicit redaction helpers used by the codebase.
	logx.From(req.Context()).Info("connection created",
		"api_key", logx.RedactToken(secret),
		"email", logx.RedactEmail("ops@example.com"),
	)

	out := buf.String() + rr.Body.String()
	if strings.Contains(out, secret) {
		t.Fatalf("captured output leaked secret: %s", out)
	}
	_ = io.Discard
}
