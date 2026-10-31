// Package dbtest provides migrated, isolated databases for tests.
package dbtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/builtly/builtly/internal/platform/db"
)

// postgresTestLockKey serializes tests that share builtly_test across packages.
// go test ./... runs packages in parallel; without this, concurrent
// DROP SCHEMA CASCADE + MigrateUp races (e.g. "relation purchases does not exist").
const postgresTestLockKey int64 = 872014401

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

	maxOpen := 5
	database, err := db.Open(driver, dsn, maxOpen)
	if err != nil {
		t.Fatalf("dbtest: open %s: %v", driver, err)
	}

	ctx := context.Background()
	if driver == "postgres" {
		lockConn, err := database.DB.Conn(ctx)
		if err != nil {
			_ = database.Close()
			t.Fatalf("dbtest: lock conn: %v", err)
		}
		if _, err := lockConn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, postgresTestLockKey); err != nil {
			_ = lockConn.Close()
			_ = database.Close()
			t.Fatalf("dbtest: advisory lock: %v", err)
		}
		t.Cleanup(func() {
			_, _ = lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, postgresTestLockKey)
			_ = lockConn.Close()
			_ = database.Close()
		})
		if _, err := lockConn.ExecContext(ctx, `
			DROP SCHEMA public CASCADE;
			CREATE SCHEMA public;
			GRANT ALL ON SCHEMA public TO public;
		`); err != nil {
			t.Fatalf("dbtest: reset postgres: %v", err)
		}
	} else {
		t.Cleanup(func() { _ = database.Close() })
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
