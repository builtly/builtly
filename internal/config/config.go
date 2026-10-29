// Package config loads and validates process configuration once at startup.
package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated process configuration.
type Config struct {
	Env             string
	HTTPAddr        string
	PublicURL       string
	DBDriver        string
	DBDSN           string
	DBMaxOpenConns  int
	AutoMigrate     bool
	JWTSigningKeys  []KeyMaterial // first is active for signing
	JWTAccessTTL    time.Duration
	JWTRefreshTTL   time.Duration
	EncryptionKeys  []KeyMaterial // first is active for sealing
	AdminEnabled    bool
	AdminToken      string
	AdminAllowed    []*net.IPNet
	WorkersEnabled  bool
	TrustedProxies  []*net.IPNet
	CORSMaxAge      int
	LogLevel        string
	LogFormat       string
	ShutdownTimeout time.Duration

	// DataDir is where builtly.json and optional SQLite live.
	DataDir string
	// NeedsSetup is true when core config is incomplete or DB is unreachable.
	NeedsSetup bool
	// SetupReason is "unconfigured" or "db_unreachable".
	SetupReason string
	// SetupPreserveSecrets tells the wizard not to rotate JWT/encryption/admin keys.
	SetupPreserveSecrets bool
	// SetupDBError is the last DB ping/open error when SetupReason is db_unreachable.
	SetupDBError string
	// SetupToken authorizes setup API calls while NeedsSetup is true.
	SetupToken string
	// Persistent marks whether data-dir settings were loaded or written.
	Persistent PersistentSettings

	// DefaultConnections holds env-fallback provider credentials (nil groups = unset).
	DefaultConnections DefaultConnections
}

// KeyMaterial is a versioned cryptographic key (JWT kid or encryption version).
type KeyMaterial struct {
	ID  string
	Key []byte
	Raw string // original "id:base64" segment for diagnostics (never logged)
}

// DefaultConnections groups optional env-defined default provider credentials.
type DefaultConnections struct {
	Stripe      *StripeDefault
	Resend      *ResendDefault
	R2          *R2Default
	GoogleOAuth *GoogleOAuthDefault
	GitHubOAuth *GitHubOAuthDefault
	AppleSignIn *AppleSignInDefault
	AppleIAP    *AppleIAPDefault
	GooglePlay  *GooglePlayDefault
	PostHog     *PostHogDefault
	PromoteKit  *APIKeyDefault
	Affonso     *APIKeyDefault
}

type StripeDefault struct {
	SecretKey      string
	WebhookSecrets []string
	LiveMode       bool
}

type ResendDefault struct {
	APIKey string
}

type R2Default struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
}

type GoogleOAuthDefault struct {
	ClientID        string
	ClientSecret    string
	NativeClientIDs []string
}

type GitHubOAuthDefault struct {
	ClientID     string
	ClientSecret string
}

type AppleSignInDefault struct {
	TeamID     string
	KeyID      string
	PrivateKey string
	ServicesID string
	BundleIDs  []string
}

type AppleIAPDefault struct {
	IssuerID   string
	KeyID      string
	PrivateKey string
	BundleID   string
}

type GooglePlayDefault struct {
	Package        string
	ServiceAccount string
}

type PostHogDefault struct {
	Host   string
	APIKey string
}

type APIKeyDefault struct {
	APIKey string
}

// Load reads process env (after optional .env + data-dir file), validates, and returns Config.
// Precedence: process env > data-dir file > defaults.
// Incomplete public_url / database / secrets → NeedsSetup (wizard). Callers should also
// ping the DB; on failure use MarkDBUnreachable to re-enter setup without rotating secrets.
func Load() (*Config, error) {
	envFile := getenv("BUILTLY_ENV_FILE", "./.env")
	if err := loadDotEnv(envFile); err != nil {
		return nil, err
	}

	dataDir := DataDir()
	persisted, err := LoadPersistent(dataDir)
	if err != nil {
		return nil, err
	}
	if err := applyPersistentToEnv(persisted); err != nil {
		return nil, err
	}

	cfg := &Config{
		Env:             getenv("BUILTLY_ENV", "development"),
		HTTPAddr:        getenv("BUILTLY_HTTP_ADDR", ":8080"),
		PublicURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("BUILTLY_PUBLIC_URL")), "/"),
		DBDriver:        strings.ToLower(strings.TrimSpace(os.Getenv("BUILTLY_DB_DRIVER"))),
		DBDSN:           strings.TrimSpace(os.Getenv("BUILTLY_DB_DSN")),
		DBMaxOpenConns:  getenvInt("BUILTLY_DB_MAX_OPEN_CONNS", 20),
		AutoMigrate:     getenvBool("BUILTLY_AUTO_MIGRATE", false),
		JWTAccessTTL:    getenvDuration("BUILTLY_JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:   getenvDuration("BUILTLY_REFRESH_TTL", 720*time.Hour),
		AdminEnabled:    getenvBool("BUILTLY_ADMIN_ENABLED", true),
		AdminToken:      os.Getenv("BUILTLY_ADMIN_TOKEN"),
		WorkersEnabled:  getenvBool("BUILTLY_WORKERS_ENABLED", true),
		CORSMaxAge:      getenvInt("BUILTLY_CORS_MAX_AGE", 600),
		LogLevel:        getenv("BUILTLY_LOG_LEVEL", "info"),
		LogFormat:       getenv("BUILTLY_LOG_FORMAT", "json"),
		ShutdownTimeout: getenvDuration("BUILTLY_SHUTDOWN_TIMEOUT", 15*time.Second),
		DataDir:         dataDir,
		Persistent:      persisted,
	}

	switch cfg.Env {
	case "development", "production":
	default:
		return nil, fmt.Errorf("BUILTLY_ENV must be development or production")
	}

	missingSecrets := os.Getenv("BUILTLY_JWT_SIGNING_KEYS") == "" ||
		os.Getenv("BUILTLY_ENCRYPTION_KEYS") == "" ||
		(cfg.AdminEnabled && len(cfg.AdminToken) < 32)
	missingCore := cfg.PublicURL == "" || cfg.DBDriver == "" || cfg.DBDSN == ""

	if missingCore || missingSecrets {
		return cfg.enterSetup("unconfigured", false, ""), nil
	}

	if cfg.Env == "production" && cfg.LogFormat == "text" {
		return nil, fmt.Errorf("BUILTLY_LOG_FORMAT=text is forbidden in production")
	}

	switch cfg.DBDriver {
	case "postgres", "postgresql", "sqlite", "libsql", "turso":
		if cfg.DBDriver == "postgresql" {
			cfg.DBDriver = "postgres"
		}
		if cfg.DBDriver == "turso" {
			cfg.DBDriver = "libsql"
		}
	default:
		return nil, fmt.Errorf("BUILTLY_DB_DRIVER must be postgres, sqlite, or libsql")
	}

	cfg.JWTSigningKeys, err = parseKeyList("BUILTLY_JWT_SIGNING_KEYS", os.Getenv("BUILTLY_JWT_SIGNING_KEYS"), 32)
	if err != nil {
		return nil, err
	}
	cfg.EncryptionKeys, err = parseKeyList("BUILTLY_ENCRYPTION_KEYS", os.Getenv("BUILTLY_ENCRYPTION_KEYS"), 32)
	if err != nil {
		return nil, err
	}

	if cfg.AdminEnabled {
		if len(cfg.AdminToken) < 32 {
			return nil, fmt.Errorf("BUILTLY_ADMIN_TOKEN must be at least 32 characters when admin is enabled")
		}
	}
	cfg.AdminAllowed, err = parseCIDRs("BUILTLY_ADMIN_ALLOWED_CIDRS", os.Getenv("BUILTLY_ADMIN_ALLOWED_CIDRS"))
	if err != nil {
		return nil, err
	}
	cfg.TrustedProxies, err = parseCIDRs("BUILTLY_TRUSTED_PROXIES", os.Getenv("BUILTLY_TRUSTED_PROXIES"))
	if err != nil {
		return nil, err
	}

	if err := cfg.loadDefaultConnections(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// MarkDBUnreachable switches a fully loaded config into setup mode after a failed DB open/ping.
// Existing secrets are preserved so a temporary outage cannot rotate keys.
func (c *Config) MarkDBUnreachable(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	_ = c.enterSetup("db_unreachable", true, msg)
}

func (c *Config) enterSetup(reason string, preserveSecrets bool, dbErr string) *Config {
	c.NeedsSetup = true
	c.SetupReason = reason
	c.SetupPreserveSecrets = preserveSecrets
	c.SetupDBError = dbErr
	c.SetupToken = strings.TrimSpace(os.Getenv("BUILTLY_SETUP_TOKEN"))
	if c.SetupToken == "" {
		if tok, err := GenerateAdminToken(); err == nil {
			c.SetupToken = tok
		}
	}
	if c.LogFormat == "" {
		c.LogFormat = "json"
	}
	return c
}

// HasSecrets reports whether JWT, encryption, and admin credentials are present in env/config.
func (c *Config) HasSecrets() bool {
	if os.Getenv("BUILTLY_JWT_SIGNING_KEYS") == "" || os.Getenv("BUILTLY_ENCRYPTION_KEYS") == "" {
		return false
	}
	if c.AdminEnabled && len(c.AdminToken) < 32 {
		return false
	}
	return true
}

func (c *Config) loadDefaultConnections() error {
	var err error
	c.DefaultConnections.Stripe, err = loadGroup("stripe", []string{
		"BUILTLY_STRIPE_SECRET_KEY",
		"BUILTLY_STRIPE_WEBHOOK_SECRETS",
		"BUILTLY_STRIPE_LIVEMODE",
	}, func() (*StripeDefault, error) {
		return &StripeDefault{
			SecretKey:      os.Getenv("BUILTLY_STRIPE_SECRET_KEY"),
			WebhookSecrets: splitCSV(os.Getenv("BUILTLY_STRIPE_WEBHOOK_SECRETS")),
			LiveMode:       getenvBool("BUILTLY_STRIPE_LIVEMODE", false),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.Resend, err = loadGroup("resend", []string{"BUILTLY_RESEND_API_KEY"}, func() (*ResendDefault, error) {
		return &ResendDefault{APIKey: os.Getenv("BUILTLY_RESEND_API_KEY")}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.R2, err = loadGroup("r2", []string{
		"BUILTLY_R2_ACCOUNT_ID", "BUILTLY_R2_ACCESS_KEY_ID", "BUILTLY_R2_SECRET_ACCESS_KEY", "BUILTLY_R2_BUCKET",
	}, func() (*R2Default, error) {
		return &R2Default{
			AccountID:       os.Getenv("BUILTLY_R2_ACCOUNT_ID"),
			AccessKeyID:     os.Getenv("BUILTLY_R2_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("BUILTLY_R2_SECRET_ACCESS_KEY"),
			Bucket:          os.Getenv("BUILTLY_R2_BUCKET"),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.GoogleOAuth, err = loadGroup("google_oauth", []string{
		"BUILTLY_GOOGLE_CLIENT_ID", "BUILTLY_GOOGLE_CLIENT_SECRET",
	}, func() (*GoogleOAuthDefault, error) {
		return &GoogleOAuthDefault{
			ClientID:        os.Getenv("BUILTLY_GOOGLE_CLIENT_ID"),
			ClientSecret:    os.Getenv("BUILTLY_GOOGLE_CLIENT_SECRET"),
			NativeClientIDs: splitCSV(os.Getenv("BUILTLY_GOOGLE_NATIVE_CLIENT_IDS")),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.GitHubOAuth, err = loadGroup("github_oauth", []string{
		"BUILTLY_GITHUB_CLIENT_ID", "BUILTLY_GITHUB_CLIENT_SECRET",
	}, func() (*GitHubOAuthDefault, error) {
		return &GitHubOAuthDefault{
			ClientID:     os.Getenv("BUILTLY_GITHUB_CLIENT_ID"),
			ClientSecret: os.Getenv("BUILTLY_GITHUB_CLIENT_SECRET"),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.AppleSignIn, err = loadGroup("apple_signin", []string{
		"BUILTLY_APPLE_TEAM_ID", "BUILTLY_APPLE_SIGNIN_KEY_ID", "BUILTLY_APPLE_SIGNIN_PRIVATE_KEY", "BUILTLY_APPLE_SERVICES_ID",
	}, func() (*AppleSignInDefault, error) {
		pk, err := readMaybeFile(os.Getenv("BUILTLY_APPLE_SIGNIN_PRIVATE_KEY"))
		if err != nil {
			return nil, err
		}
		return &AppleSignInDefault{
			TeamID:     os.Getenv("BUILTLY_APPLE_TEAM_ID"),
			KeyID:      os.Getenv("BUILTLY_APPLE_SIGNIN_KEY_ID"),
			PrivateKey: pk,
			ServicesID: os.Getenv("BUILTLY_APPLE_SERVICES_ID"),
			BundleIDs:  splitCSV(os.Getenv("BUILTLY_APPLE_BUNDLE_IDS")),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.AppleIAP, err = loadGroup("apple_iap", []string{
		"BUILTLY_APPLE_IAP_ISSUER_ID", "BUILTLY_APPLE_IAP_KEY_ID", "BUILTLY_APPLE_IAP_PRIVATE_KEY", "BUILTLY_APPLE_IAP_BUNDLE_ID",
	}, func() (*AppleIAPDefault, error) {
		pk, err := readMaybeFile(os.Getenv("BUILTLY_APPLE_IAP_PRIVATE_KEY"))
		if err != nil {
			return nil, err
		}
		return &AppleIAPDefault{
			IssuerID:   os.Getenv("BUILTLY_APPLE_IAP_ISSUER_ID"),
			KeyID:      os.Getenv("BUILTLY_APPLE_IAP_KEY_ID"),
			PrivateKey: pk,
			BundleID:   os.Getenv("BUILTLY_APPLE_IAP_BUNDLE_ID"),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.GooglePlay, err = loadGroup("google_play", []string{
		"BUILTLY_GOOGLE_PLAY_PACKAGE", "BUILTLY_GOOGLE_PLAY_SERVICE_ACCOUNT",
	}, func() (*GooglePlayDefault, error) {
		sa, err := readMaybeFile(os.Getenv("BUILTLY_GOOGLE_PLAY_SERVICE_ACCOUNT"))
		if err != nil {
			return nil, err
		}
		return &GooglePlayDefault{
			Package:        os.Getenv("BUILTLY_GOOGLE_PLAY_PACKAGE"),
			ServiceAccount: sa,
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.PostHog, err = loadGroup("posthog", []string{
		"BUILTLY_POSTHOG_HOST", "BUILTLY_POSTHOG_API_KEY",
	}, func() (*PostHogDefault, error) {
		return &PostHogDefault{
			Host:   os.Getenv("BUILTLY_POSTHOG_HOST"),
			APIKey: os.Getenv("BUILTLY_POSTHOG_API_KEY"),
		}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.PromoteKit, err = loadGroup("promotekit", []string{"BUILTLY_PROMOTEKIT_API_KEY"}, func() (*APIKeyDefault, error) {
		return &APIKeyDefault{APIKey: os.Getenv("BUILTLY_PROMOTEKIT_API_KEY")}, nil
	})
	if err != nil {
		return err
	}
	c.DefaultConnections.Affonso, err = loadGroup("affonso", []string{"BUILTLY_AFFONSO_API_KEY"}, func() (*APIKeyDefault, error) {
		return &APIKeyDefault{APIKey: os.Getenv("BUILTLY_AFFONSO_API_KEY")}, nil
	})
	return err
}

func loadGroup[T any](name string, keys []string, build func() (*T, error)) (*T, error) {
	set, unset := 0, 0
	for _, k := range keys {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			set++
		} else {
			unset++
		}
	}
	if set == 0 {
		return nil, nil
	}
	if unset > 0 {
		return nil, fmt.Errorf("partial %s default connection: set all of %s or none", name, strings.Join(keys, ", "))
	}
	return build()
}

func parseKeyList(name, raw string, minBytes int) ([]KeyMaterial, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	parts := strings.Split(raw, ",")
	out := make([]KeyMaterial, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, b64, ok := strings.Cut(p, ":")
		if !ok || id == "" || b64 == "" {
			return nil, fmt.Errorf("%s entries must be id:base64", name)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%s: duplicate key id %q", name, id)
		}
		seen[id] = struct{}{}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("%s: decode key %q: %w", name, id, err)
		}
		if len(key) < minBytes {
			return nil, fmt.Errorf("%s: key %q must be at least %d bytes", name, id, minBytes)
		}
		out = append(out, KeyMaterial{ID: id, Key: key, Raw: p})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s is required", name)
	}
	return out, nil
}

func parseCIDRs(name, raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, p := range splitCSV(raw) {
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid CIDR %q: %w", name, p, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func readMaybeFile(v string) (string, error) {
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return v, nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvBool(k string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func getenvDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// IsProduction reports whether the process runs in production mode.
func (c *Config) IsProduction() bool { return c.Env == "production" }
