package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadPersistent(t *testing.T) {
	dir := t.TempDir()
	auto := true
	in := PersistentSettings{
		PublicURL:      "https://api.example.com",
		DBDriver:       "postgres",
		DBDSN:          "postgres://u:p@h/db",
		JWTSigningKeys: "v1:YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		EncryptionKeys: "1:YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY=",
		AdminToken:     "01234567890123456789012345678901",
		AutoMigrate:    &auto,
		Configured:     true,
	}
	if err := SavePersistent(dir, in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, persistFileName)); err != nil {
		t.Fatal(err)
	}
	out, err := LoadPersistent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.PublicURL != in.PublicURL || out.DBDriver != in.DBDriver || !out.Configured {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestGenerateSetupSecrets(t *testing.T) {
	jwt, enc, admin, err := GenerateSetupSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if jwt == "" || enc == "" || len(admin) < 32 {
		t.Fatalf("jwt=%q enc=%q admin=%q", jwt, enc, admin)
	}
}
