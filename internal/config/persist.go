package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const persistFileName = "builtly.json"

// PersistentSettings is the subset of process config stored under the data dir.
// Process env always wins over these values.
type PersistentSettings struct {
	PublicURL      string `json:"public_url,omitempty"`
	DBDriver       string `json:"db_driver,omitempty"`
	DBDSN          string `json:"db_dsn,omitempty"`
	JWTSigningKeys string `json:"jwt_signing_keys,omitempty"`
	EncryptionKeys string `json:"encryption_keys,omitempty"`
	AdminToken     string `json:"admin_token,omitempty"`
	AutoMigrate    *bool  `json:"auto_migrate,omitempty"`
	Configured     bool   `json:"configured,omitempty"`
}

// DataDir returns BUILTLY_DATA_DIR or ./data.
func DataDir() string {
	return getenv("BUILTLY_DATA_DIR", "./data")
}

func persistPath(dir string) string {
	return filepath.Join(dir, persistFileName)
}

// LoadPersistent reads data-dir settings. Missing file returns zero value.
func LoadPersistent(dir string) (PersistentSettings, error) {
	path := persistPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PersistentSettings{}, nil
		}
		return PersistentSettings{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s PersistentSettings
	if err := json.Unmarshal(b, &s); err != nil {
		return PersistentSettings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// SavePersistent writes settings atomically under dir.
func SavePersistent(dir string, s PersistentSettings) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := persistPath(dir)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// applyPersistentToEnv sets process env from the data file only for keys that
// are not already present (same precedence as .env: process env wins).
func applyPersistentToEnv(s PersistentSettings) error {
	set := func(key, val string) error {
		if val == "" {
			return nil
		}
		if _, exists := os.LookupEnv(key); exists {
			return nil
		}
		return os.Setenv(key, val)
	}
	if err := set("BUILTLY_PUBLIC_URL", s.PublicURL); err != nil {
		return err
	}
	if err := set("BUILTLY_DB_DRIVER", s.DBDriver); err != nil {
		return err
	}
	if err := set("BUILTLY_DB_DSN", s.DBDSN); err != nil {
		return err
	}
	if err := set("BUILTLY_JWT_SIGNING_KEYS", s.JWTSigningKeys); err != nil {
		return err
	}
	if err := set("BUILTLY_ENCRYPTION_KEYS", s.EncryptionKeys); err != nil {
		return err
	}
	if err := set("BUILTLY_ADMIN_TOKEN", s.AdminToken); err != nil {
		return err
	}
	if s.AutoMigrate != nil {
		v := "false"
		if *s.AutoMigrate {
			v = "true"
		}
		if err := set("BUILTLY_AUTO_MIGRATE", v); err != nil {
			return err
		}
	}
	return nil
}
