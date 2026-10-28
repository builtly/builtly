package referral_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/tenant"
)

func TestReferralWindowClosed(t *testing.T) {
	h := setupRef(t)
	s := tenant.DefaultSettings()
	s.AllowedOrigins = []string{"https://win.com"}
	s.Auth.PasswordEnabled = true
	s.Email = tenant.EmailSettings{FromAddress: "n@win.com", ActionBaseURL: "https://win.com"}
	s.Referral.Enabled = true
	s.Referral.ApplyWindowDays = 1
	s.Referral.RefereeBonusCredits = 5
	app, err := h.tSvc.Create(context.Background(), "win", "Win", s)
	if err != nil {
		t.Fatal(err)
	}
	refID, _ := h.register(t, app, "ref@win.com", "")
	code := h.codeOf(t, app.ID, refID)

	eeID, _ := h.register(t, app, "late@win.com", "")
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := h.db.ExecContext(context.Background(), h.db.Rebind(`
		UPDATE users SET created_at = ? WHERE app_id = ? AND id = ?`),
		old, app.ID.String(), eeID.String()); err != nil {
		t.Fatal(err)
	}

	login, err := h.authSvc.Login(context.Background(), app, auth.LoginInput{
		Email: "late@win.com", Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code})
	req := httptest.NewRequest("POST", "/api/v1/user/referral/apply", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+login.Tokens.AccessToken)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 422 {
		t.Fatalf("window closed want 422, got %d %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "referral_window_closed") {
		t.Fatalf("body %s", rr.Body.String())
	}
}
