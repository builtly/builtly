// Package dbtest provides migrated, isolated databases for tests.
package dbtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtly/builtly/internal/platform/db"
)

// New opens a fresh migrated database for driver ("postgres"|"sqlite").
// For postgres, set BUILTLY_TEST_DATABASE_URL
// (default postgres://builtly:builtly@127.0.0.1:5432/builtly_test?sslmode=disable).
func New(t *testing.T, driver string) *db.DB {
	t.Helper()
	var dsn string
	switch driver {
	case "sqlite":
		dir := t.TempDir()
		dsn = "file:" + filepath.Join(dir, "test.db")
	case "postgres":
		dsn = os.Getenv("BUILTLY_TEST_DATABASE_URL")
		if dsn == "" {
			dsn = "postgres://builtly:builtly@127.0.0.1:5432/builtly_test?sslmode=disable"
		}
	default:
		t.Fatalf("dbtest: unsupported driver %q", driver)
	}
	database, err := db.Open(driver, dsn, 5)
	if err != nil {
		t.Fatalf("dbtest: open %s: %v", driver, err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	if driver == "postgres" {
		if _, err := database.ExecContext(ctx, `
			DROP SCHEMA public CASCADE;
			CREATE SCHEMA public;
			GRANT ALL ON SCHEMA public TO public;
		`); err != nil {
			t.Fatalf("dbtest: reset postgres: %v", err)
		}
	}
	if err := database.MigrateUp(ctx); err != nil {
		t.Fatalf("dbtest: migrate %s: %v", driver, err)
	}
	return database
}

// Drivers returns the dialects to exercise in store/service tests.
func Drivers() []string {
	return []string{"sqlite", "postgres"}
}
