package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tenant-owned tables that MUST include app_id in every store SQL constant.
var tenantTables = []string{
	"users", "user_identities", "refresh_tokens", "email_tokens",
	"products", "purchases", "credit_balances", "credit_ledger",
	"referrals", "files", "app_api_keys",
	"email_templates", "email_trigger_bindings", "email_sends",
}

// TestTenantSQLLint scans internal/*/store.go SQL string constants and fails if a
// statement that mentions a tenant-owned table lacks app_id. With no store files
// yet (M0/M1), this is a no-op pass that documents the contract.
func TestTenantSQLLint(t *testing.T) {
	// Package dir is internal/platform/db; walk internal/ for store.go files.
	internalDir := filepath.Join("..", "..")
	var storeFiles []string
	_ = filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if info.Name() == "store.go" {
			storeFiles = append(storeFiles, path)
		}
		return nil
	})

	fset := token.NewFileSet()
	for _, path := range storeFiles {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			sql := strings.Trim(bl.Value, "`\"")
			lower := strings.ToLower(sql)
			if !strings.Contains(lower, "select") && !strings.Contains(lower, "insert") &&
				!strings.Contains(lower, "update") && !strings.Contains(lower, "delete") {
				return true
			}
			for _, table := range tenantTables {
				if !containsTable(lower, table) {
					continue
				}
				if !strings.Contains(lower, "app_id") {
					t.Errorf("%s: SQL referencing %s lacks app_id:\n%s", path, table, sql)
				}
			}
			return true
		})
	}
}

func containsTable(sql, table string) bool {
	// crude token check
	for _, sep := range []string{" " + table + " ", " " + table + "(", " " + table + "\n", "into " + table, "from " + table, "update " + table} {
		if strings.Contains(sql, sep) {
			return true
		}
	}
	return false
}
