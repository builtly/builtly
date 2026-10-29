// Package setup serves the first-boot configuration wizard.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
)

// Handler serves /api/v1/setup/* while NeedsSetup is true.
type Handler struct {
	Cfg    *config.Config
	OnDone func()
}

// Mount registers setup routes on a chi router.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/v1/setup/status", h.status)
	r.Post("/api/v1/setup/test-db", h.testDB)
	r.Post("/api/v1/setup/complete", h.complete)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	needs := h.Cfg != nil && h.Cfg.NeedsSetup
	out := map[string]any{
		"needs_setup":    needs,
		"requires_token": needs && h.Cfg != nil && h.Cfg.SetupToken != "",
		"data_dir":       config.DataDir(),
		"suggested_url":  suggestedPublicURL(r),
		"default_sqlite": defaultSQLiteDSN(config.DataDir()),
		"drivers": []map[string]string{
			{"id": "sqlite", "label": "SQLite (local file)", "hint": "Single node only; stored on the data volume"},
			{"id": "libsql", "label": "SQLite cloud (Turso / libSQL)", "hint": "Multi-node OK; paste libsql://…?authToken=…"},
			{"id": "postgres", "label": "Postgres", "hint": "Neon / Supabase / self-hosted"},
		},
	}
	if h.Cfg != nil {
		out["reason"] = h.Cfg.SetupReason
		out["preserve_secrets"] = h.Cfg.SetupPreserveSecrets
		out["db_error"] = h.Cfg.SetupDBError
		out["public_url"] = h.Cfg.PublicURL
		out["db_driver"] = h.Cfg.DBDriver
		// Never put raw DSN (passwords / Turso tokens) on the unauthenticated status route.
		if h.tokenOK(r, "") {
			out["db_dsn"] = h.Cfg.DBDSN
		} else if strings.TrimSpace(h.Cfg.DBDSN) != "" {
			out["db_dsn_set"] = true
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type dbBody struct {
	SetupToken string `json:"setup_token"`
	Driver     string `json:"driver"`
	DSN        string `json:"dsn"`
}

type completeBody struct {
	SetupToken string `json:"setup_token"`
	PublicURL  string `json:"public_url"`
	Driver     string `json:"driver"`
	DSN        string `json:"dsn"`
}

func (h *Handler) testDB(w http.ResponseWriter, r *http.Request) {
	if h.Cfg == nil || !h.Cfg.NeedsSetup {
		writeErr(w, http.StatusConflict, "already_configured", "setup is not available")
		return
	}
	var body dbBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.tokenOK(r, body.SetupToken) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid setup token")
		return
	}
	driver, dsn, err := normalizeDB(body.Driver, body.DSN, config.DataDir())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := pingDB(ctx, driver, dsn); err != nil {
		writeErr(w, http.StatusBadRequest, "db_unreachable", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "driver": driver, "dsn": dsn})
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	if h.Cfg == nil || !h.Cfg.NeedsSetup {
		writeErr(w, http.StatusConflict, "already_configured", "setup is not available")
		return
	}
	var body completeBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.tokenOK(r, body.SetupToken) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid setup token")
		return
	}
	publicURL := strings.TrimRight(strings.TrimSpace(body.PublicURL), "/")
	if publicURL == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "public_url is required")
		return
	}
	if _, err := url.ParseRequestURI(publicURL); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "public_url must be an absolute URL")
		return
	}
	driver, dsn, err := normalizeDB(body.Driver, body.DSN, config.DataDir())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	jwtKeys, encKeys, adminToken, err := h.resolveSecrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Open + migrate before writing configured=true so a failed migrate cannot
	// leave the process unable to re-enter the wizard after restart.
	database, err := db.Open(driver, dsn, 5)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "db_unreachable", err.Error())
		return
	}
	defer database.Close()
	if err := database.MigrateUp(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "migrate_failed", err.Error())
		return
	}

	auto := true
	settings := config.PersistentSettings{
		PublicURL:      publicURL,
		DBDriver:       driver,
		DBDSN:          dsn,
		JWTSigningKeys: jwtKeys,
		EncryptionKeys: encKeys,
		AdminToken:     adminToken,
		AutoMigrate:    &auto,
		Configured:     true,
	}
	if err := config.SavePersistent(config.DataDir(), settings); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	_ = os.Setenv("BUILTLY_PUBLIC_URL", publicURL)
	_ = os.Setenv("BUILTLY_DB_DRIVER", driver)
	_ = os.Setenv("BUILTLY_DB_DSN", dsn)
	_ = os.Setenv("BUILTLY_JWT_SIGNING_KEYS", jwtKeys)
	_ = os.Setenv("BUILTLY_ENCRYPTION_KEYS", encKeys)
	_ = os.Setenv("BUILTLY_ADMIN_TOKEN", adminToken)
	_ = os.Setenv("BUILTLY_AUTO_MIGRATE", "true")

	envMap := map[string]string{
		"BUILTLY_PUBLIC_URL":       publicURL,
		"BUILTLY_DB_DRIVER":        driver,
		"BUILTLY_DB_DSN":           dsn,
		"BUILTLY_JWT_SIGNING_KEYS": jwtKeys,
		"BUILTLY_ENCRYPTION_KEYS":  encKeys,
		"BUILTLY_ADMIN_TOKEN":      adminToken,
		"BUILTLY_AUTO_MIGRATE":     "true",
	}
	cfgJSON, _ := json.MarshalIndent(settings, "", "  ")

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"admin_token": adminToken,
		"public_url":  publicURL,
		"db_driver":   driver,
		"node_scope":  "This save applies to the current node’s data dir only.",
		"multi_node":  "For Fly.io / multi-node, copy the env block or download builtly.json into every machine’s Secrets (same values everywhere). Do not use local SQLite across replicas.",
		"env":         envMap,
		"config_json": string(cfgJSON),
		"hint":        "Single-node: data-dir file is enough. Multi-node: paste env into platform Secrets.",
	})
	if h.OnDone != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			h.OnDone()
		}()
	}
}

func (h *Handler) resolveSecrets() (jwtKeys, encKeys, adminToken string, err error) {
	preserve := h.Cfg != nil && h.Cfg.SetupPreserveSecrets
	jwtKeys = os.Getenv("BUILTLY_JWT_SIGNING_KEYS")
	encKeys = os.Getenv("BUILTLY_ENCRYPTION_KEYS")
	adminToken = ""
	if h.Cfg != nil {
		adminToken = h.Cfg.AdminToken
	}
	if adminToken == "" {
		adminToken = os.Getenv("BUILTLY_ADMIN_TOKEN")
	}
	if preserve && jwtKeys != "" && encKeys != "" && len(adminToken) >= 32 {
		return jwtKeys, encKeys, adminToken, nil
	}
	genJWT, genEnc, genAdmin, err := config.GenerateSetupSecrets()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate secrets")
	}
	if jwtKeys == "" {
		jwtKeys = genJWT
	}
	if encKeys == "" {
		encKeys = genEnc
	}
	if len(adminToken) < 32 {
		adminToken = genAdmin
	}
	return jwtKeys, encKeys, adminToken, nil
}

func (h *Handler) tokenOK(r *http.Request, bodyToken string) bool {
	expected := ""
	if h.Cfg != nil {
		expected = h.Cfg.SetupToken
	}
	if expected == "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("X-Builtly-Setup-Token"))
	if got == "" {
		got = strings.TrimSpace(bodyToken)
	}
	return got == expected
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	httpx.WriteJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func suggestedPublicURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return proto + "://" + host
}

func defaultSQLiteDSN(dataDir string) string {
	return "file:" + filepath.ToSlash(filepath.Join(dataDir, "builtly.db"))
}

func normalizeDB(driver, dsn, dataDir string) (string, string, error) {
	driver = strings.TrimSpace(strings.ToLower(driver))
	dsn = strings.TrimSpace(dsn)
	switch driver {
	case "sqlite":
		if dsn == "" {
			dsn = defaultSQLiteDSN(dataDir)
		}
		return driver, dsn, nil
	case "libsql", "turso":
		if dsn == "" {
			return "", "", fmt.Errorf("libsql/Turso URL is required (libsql://…?authToken=…)")
		}
		if !strings.Contains(dsn, "://") {
			return "", "", fmt.Errorf("libsql DSN must be a URL (libsql:// or https://)")
		}
		return "libsql", dsn, nil
	case "postgres", "postgresql":
		if dsn == "" {
			return "", "", fmt.Errorf("postgres dsn is required")
		}
		return "postgres", dsn, nil
	default:
		return "", "", fmt.Errorf("driver must be sqlite, libsql, or postgres")
	}
}

func pingDB(ctx context.Context, driver, dsn string) error {
	database, err := db.Open(driver, dsn, 2)
	if err != nil {
		return err
	}
	defer database.Close()
	return database.PingContext(ctx)
}
