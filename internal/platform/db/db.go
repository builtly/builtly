// Package db wraps sqlx with dialect-aware helpers, transactions, and migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"                   // postgres
	_ "github.com/tursodatabase/libsql-client-go/libsql" // turso / libsql
	_ "modernc.org/sqlite"                               // local sqlite
)

// DB is the process-wide database handle.
type DB struct {
	*sqlx.DB
	// Driver is the SQL dialect used for migrations and binds: "postgres" or "sqlite".
	Driver string
}

// Querier is satisfied by *sqlx.DB and *sqlx.Tx.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error)
	Rebind(query string) string
}

// Dialect returns the migration/bind dialect for a configured driver name.
func Dialect(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "postgres", "postgresql":
		return "postgres"
	case "sqlite", "libsql", "turso":
		return "sqlite"
	default:
		return ""
	}
}

// Open opens and pings a database for the given driver.
// Supported: postgres | sqlite (local file) | libsql/turso (remote SQLite SaaS).
func Open(driver, dsn string, maxOpen int) (*DB, error) {
	driver = strings.ToLower(strings.TrimSpace(driver))
	dsn = strings.TrimSpace(dsn)

	var (
		sqlDriver string
		dsnFinal  string
		dialect   string
	)
	switch driver {
	case "postgres", "postgresql":
		sqlDriver = "pgx"
		dsnFinal = dsn
		dialect = "postgres"
	case "sqlite":
		sqlDriver = "sqlite"
		dsnFinal = withSQLitePragmas(dsn)
		dialect = "sqlite"
	case "libsql", "turso":
		sqlDriver = "libsql"
		dsnFinal = dsn
		dialect = "sqlite"
	default:
		return nil, fmt.Errorf("db: unsupported driver %q", driver)
	}

	sqldb, err := sql.Open(sqlDriver, dsnFinal)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	// sqlx binder: use sqlite for libsql (? placeholders).
	bindName := sqlDriver
	if dialect == "sqlite" {
		bindName = "sqlite"
	}
	x := sqlx.NewDb(sqldb, bindName)
	if dialect == "sqlite" && driver == "sqlite" {
		x.SetMaxOpenConns(1)
	} else {
		if maxOpen <= 0 {
			maxOpen = 20
		}
		maxIdle := maxOpen / 2
		if maxIdle < 1 {
			maxIdle = 1
		}
		x.SetMaxOpenConns(maxOpen)
		x.SetMaxIdleConns(maxIdle)
		x.SetConnMaxLifetime(30 * time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := x.PingContext(ctx); err != nil {
		_ = x.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{DB: x, Driver: dialect}, nil
}

func withSQLitePragmas(dsn string) string {
	// modernc.org/sqlite: append query params for pragmas / txlock.
	sep := "?"
	if containsQ(dsn) {
		sep = "&"
	}
	return dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
}

func containsQ(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			return true
		}
	}
	return false
}

// Rebind rewrites ? placeholders for the active driver.
func (d *DB) Rebind(q string) string {
	return d.DB.Rebind(q)
}

// BoolArg returns a driver-appropriate boolean bind value.
// SQLite stores booleans as INTEGER 0/1; Postgres expects a real bool.
func (d *DB) BoolArg(v bool) any {
	if d.Driver == "sqlite" {
		if v {
			return 1
		}
		return 0
	}
	return v
}

// WithTx runs fn inside a transaction; commits on nil, rolls back on error or panic.
func (d *DB) WithTx(ctx context.Context, fn func(tx *sqlx.Tx) error) (err error) {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// Ping checks connectivity within timeout.
func (d *DB) Ping(ctx context.Context) error {
	return d.DB.PingContext(ctx)
}
