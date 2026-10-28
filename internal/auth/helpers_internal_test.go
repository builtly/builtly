package auth

import (
	"testing"
	"time"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/tenant"
)

func TestNormalizeEmailAndActionURL(t *testing.T) {
	if _, err := normalizeEmail(""); err == nil {
		t.Fatal("empty")
	}
	if _, err := normalizeEmail("not-an-email"); err == nil {
		t.Fatal("bad")
	}
	got, err := normalizeEmail("  Foo@Example.COM ")
	if err != nil || got != "foo@example.com" {
		t.Fatalf("%q %v", got, err)
	}
	app := tenant.App{Settings: tenant.Settings{Email: tenant.EmailSettings{ActionBaseURL: "https://a.com/auth/"}}}
	u := actionURL(app, "/verify", "et_x")
	if u == "" || u == "et_x" {
		t.Fatalf("url %q", u)
	}
	app2 := tenant.App{}
	_ = actionURL(app2, "/verify", "et_x")
}

func TestTokenServiceNoKeys(t *testing.T) {
	ts := NewTokenService(nil, 0)
	if ts.AccessTTL() != 15*time.Minute {
		t.Fatalf("default ttl %v", ts.AccessTTL())
	}
	if _, _, err := ts.IssueAccess(tenant.App{}.ID, tenant.App{}.ID, "pwd", false); err == nil {
		// uuid nil ok; want no keys error
		t.Fatal("want no keys")
	}
	_ = config.KeyMaterial{}
}
