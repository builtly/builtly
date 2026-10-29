package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNeedsSetupWhenIncomplete(t *testing.T) {
	t.Setenv("BUILTLY_ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("BUILTLY_DATA_DIR", t.TempDir())
	t.Setenv("BUILTLY_ENV", "production")
	t.Setenv("BUILTLY_LOG_FORMAT", "json")
	for _, k := range []string{
		"BUILTLY_PUBLIC_URL", "BUILTLY_DB_DRIVER", "BUILTLY_DB_DSN",
		"BUILTLY_JWT_SIGNING_KEYS", "BUILTLY_ENCRYPTION_KEYS", "BUILTLY_ADMIN_TOKEN", "BUILTLY_SETUP_TOKEN",
	} {
		_ = os.Unsetenv(k)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NeedsSetup {
		t.Fatal("expected NeedsSetup")
	}
	if cfg.SetupReason != "unconfigured" {
		t.Fatalf("reason=%q", cfg.SetupReason)
	}
	if cfg.SetupToken == "" {
		t.Fatal("expected setup token")
	}
}

func TestLoadNeedsSetupWhenSecretsMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BUILTLY_ENV_FILE", filepath.Join(dir, "missing.env"))
	t.Setenv("BUILTLY_DATA_DIR", dir)
	t.Setenv("BUILTLY_ENV", "development")
	t.Setenv("BUILTLY_PUBLIC_URL", "http://localhost:8080")
	t.Setenv("BUILTLY_DB_DRIVER", "sqlite")
	t.Setenv("BUILTLY_DB_DSN", "file:"+filepath.Join(dir, "t.db"))
	t.Setenv("BUILTLY_LOG_FORMAT", "text")
	for _, k := range []string{"BUILTLY_JWT_SIGNING_KEYS", "BUILTLY_ENCRYPTION_KEYS", "BUILTLY_ADMIN_TOKEN"} {
		_ = os.Unsetenv(k)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NeedsSetup || cfg.SetupReason != "unconfigured" {
		t.Fatalf("expected unconfigured setup, got needs=%v reason=%q", cfg.NeedsSetup, cfg.SetupReason)
	}
}

func TestMarkDBUnreachablePreservesSecrets(t *testing.T) {
	cfg := &Config{
		PublicURL:  "https://api.example.com",
		DBDriver:   "postgres",
		DBDSN:      "postgres://x",
		AdminToken: "01234567890123456789012345678901",
	}
	cfg.MarkDBUnreachable(os.ErrClosed)
	if !cfg.NeedsSetup || cfg.SetupReason != "db_unreachable" || !cfg.SetupPreserveSecrets {
		t.Fatalf("%+v", cfg)
	}
}
