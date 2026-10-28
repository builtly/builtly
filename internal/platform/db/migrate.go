package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/builtly/builtly/migrations"
)

// MigrateUp applies all pending embedded migrations for the DB dialect.
func (d *DB) MigrateUp(ctx context.Context) error {
	if err := d.ensureMigrationsTable(ctx); err != nil {
		return err
	}
	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return err
	}
	files, err := migrationFiles(d.Driver)
	if err != nil {
		return err
	}
	for _, f := range files {
		ver := migrationVersion(f)
		if _, ok := applied[ver]; ok {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			return fmt.Errorf("db: read migration %s: %w", f, err)
		}
		if err := d.applyOne(ctx, ver, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// Pending returns migration versions not yet applied.
func (d *DB) Pending(ctx context.Context) ([]string, error) {
	if err := d.ensureMigrationsTable(ctx); err != nil {
		return nil, err
	}
	applied, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	files, err := migrationFiles(d.Driver)
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, f := range files {
		ver := migrationVersion(f)
		if _, ok := applied[ver]; !ok {
			pending = append(pending, ver)
		}
	}
	return pending, nil
}

// Status lists applied and pending migration versions.
func (d *DB) Status(ctx context.Context) (applied, pending []string, err error) {
	if err = d.ensureMigrationsTable(ctx); err != nil {
		return nil, nil, err
	}
	appliedMap, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, nil, err
	}
	files, err := migrationFiles(d.Driver)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		ver := migrationVersion(f)
		if _, ok := appliedMap[ver]; ok {
			applied = append(applied, ver)
		} else {
			pending = append(pending, ver)
		}
	}
	return applied, pending, nil
}

func (d *DB) ensureMigrationsTable(ctx context.Context) error {
	var ddl string
	if d.Driver == "postgres" {
		ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL
		)`
	} else {
		ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`
	}
	_, err := d.ExecContext(ctx, ddl)
	return err
}

func (d *DB) appliedVersions(ctx context.Context) (map[string]struct{}, error) {
	rows, err := d.QueryxContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func (d *DB) applyOne(ctx context.Context, version, body string) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin migration %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts := splitSQL(body)
	for i, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("db: apply migration %s statement %d: %w", version, i+1, err)
		}
	}

	now := time.Now().UTC()
	var applied any = now
	if d.Driver == "sqlite" {
		applied = now.Format(time.RFC3339Nano)
	}
	q := tx.Rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`)
	if _, err := tx.ExecContext(ctx, q, version, applied); err != nil {
		return fmt.Errorf("db: record migration %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit migration %s: %w", version, err)
	}
	return nil
}

// splitSQL splits a migration script into individual statements, respecting
// PostgreSQL dollar-quoting, SQLite BEGIN…END trigger bodies, and line comments.
func splitSQL(script string) []string {
	var (
		stmts      []string
		buf        strings.Builder
		i          int
		inDol      bool
		dolTag     string
		beginDepth int
	)
	isIdentBoundary := func(b byte) bool {
		return !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_')
	}
	matchWord := func(pos int, word string) bool {
		if pos+len(word) > len(script) {
			return false
		}
		if !strings.EqualFold(script[pos:pos+len(word)], word) {
			return false
		}
		if pos > 0 && !isIdentBoundary(script[pos-1]) {
			return false
		}
		if pos+len(word) < len(script) && !isIdentBoundary(script[pos+len(word)]) {
			return false
		}
		return true
	}
	for i < len(script) {
		if !inDol && script[i] == '-' && i+1 < len(script) && script[i+1] == '-' {
			for i < len(script) && script[i] != '\n' {
				i++
			}
			continue
		}
		if !inDol && script[i] == '$' {
			j := i + 1
			for j < len(script) && ((script[j] >= 'a' && script[j] <= 'z') || (script[j] >= 'A' && script[j] <= 'Z') || (script[j] >= '0' && script[j] <= '9') || script[j] == '_') {
				j++
			}
			if j < len(script) && script[j] == '$' {
				tag := script[i : j+1]
				if !inDol {
					inDol = true
					dolTag = tag
					buf.WriteString(tag)
					i = j + 1
					continue
				}
			}
		}
		if inDol {
			if strings.HasPrefix(script[i:], dolTag) {
				buf.WriteString(dolTag)
				i += len(dolTag)
				inDol = false
				dolTag = ""
				continue
			}
			buf.WriteByte(script[i])
			i++
			continue
		}
		if matchWord(i, "BEGIN") {
			beginDepth++
			buf.WriteString(script[i : i+5])
			i += 5
			continue
		}
		if beginDepth > 0 && matchWord(i, "END") {
			beginDepth--
			buf.WriteString(script[i : i+3])
			i += 3
			continue
		}
		if script[i] == ';' && beginDepth == 0 {
			stmt := strings.TrimSpace(buf.String())
			if stmt != "" {
				stmts = append(stmts, stmt)
			}
			buf.Reset()
			i++
			continue
		}
		buf.WriteByte(script[i])
		i++
	}
	if stmt := strings.TrimSpace(buf.String()); stmt != "" {
		stmts = append(stmts, stmt)
	}
	return stmts
}

func migrationFiles(driver string) ([]string, error) {
	subdir := "postgres"
	if driver == "sqlite" {
		subdir = "sqlite"
	}
	entries, err := fs.ReadDir(migrations.FS, subdir)
	if err != nil {
		return nil, fmt.Errorf("db: list migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, subdir+"/"+e.Name())
	}
	sort.Strings(files)
	return files, nil
}

func migrationVersion(path string) string {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	return strings.TrimSuffix(base, ".sql")
}
