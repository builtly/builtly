package logx_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/builtly/builtly/internal/platform/logx"
)

func TestRedactEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "a***@example.com",
		"A@x.io":            "a***@x.io",
		"bad":               "***",
		"":                  "***",
	}
	for in, want := range cases {
		if got := logx.RedactEmail(in); got != want {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestRedactToken(t *testing.T) {
	if logx.RedactToken("sk_live_super_secret") != "[redacted]" {
		t.Fatal("expected [redacted]")
	}
}

func TestLoggerNeverEchoesSecretsWhenRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	secret := "re_super_secret_value_do_not_leak"
	password := "hunter2password"
	jwt := "eyJhbGciOiJIUzI1NiJ9.payload.sig"
	log.Info("user registered",
		"email", logx.RedactEmail("alice@example.com"),
		"api_key", logx.RedactToken(secret),
		"password", logx.RedactToken(password),
		"access_token", logx.RedactToken(jwt),
	)
	out := buf.String()
	for _, s := range []string{secret, password, jwt, "alice@example.com"} {
		if strings.Contains(out, s) {
			t.Fatalf("log leaked %q: %s", s, out)
		}
	}
	if !strings.Contains(out, "a***@example.com") || !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected redacted fields: %s", out)
	}
}
