package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db/dbtest"
)

func TestCreditLedgerAppendOnly(t *testing.T) {
	drivers := []string{"sqlite"}
	if os.Getenv("BUILTLY_TEST_DATABASE_URL") != "" || os.Getenv("BUILTLY_TEST_POSTGRES") == "1" {
		drivers = append(drivers, "postgres")
	}
	for _, driver := range drivers {
		t.Run(driver, func(t *testing.T) {
			database := dbtest.New(t, driver)
			ctx := context.Background()

			appID := uuid.Must(uuid.NewV7())
			userID := uuid.Must(uuid.NewV7())
			ledgerID := uuid.Must(uuid.NewV7())

			now := time.Now().UTC()
			var nowArg any = now
			if driver == "sqlite" {
				nowArg = now.Format(time.RFC3339Nano)
			}

			q := database.Rebind
			_, err := database.ExecContext(ctx, q(`
				INSERT INTO apps (id, slug, name, status, settings, created_at, updated_at)
				VALUES (?, ?, 'Test', 'active', '{}', ?, ?)`),
				idArg(driver, appID), "test-app", nowArg, nowArg)
			if err != nil {
				t.Fatalf("insert app: %v", err)
			}
			_, err = database.ExecContext(ctx, q(`
				INSERT INTO users (id, app_id, email, display_name, avatar_url, status, attribution, referral_code, created_at, updated_at)
				VALUES (?, ?, 'a@example.com', '', '', 'active', '{}', 'ABCD1234', ?, ?)`),
				idArg(driver, userID), idArg(driver, appID), nowArg, nowArg)
			if err != nil {
				t.Fatalf("insert user: %v", err)
			}
			_, err = database.ExecContext(ctx, q(`
				INSERT INTO credit_balances (app_id, user_id, balance, updated_at)
				VALUES (?, ?, 10, ?)`),
				idArg(driver, appID), idArg(driver, userID), nowArg)
			if err != nil {
				t.Fatalf("insert balance: %v", err)
			}
			_, err = database.ExecContext(ctx, q(`
				INSERT INTO credit_ledger (
					id, app_id, user_id, entry_type, amount, balance_after, idempotency_key,
					reference_type, reference_id, actor, reason, metadata, created_at
				) VALUES (?, ?, ?, 'signup_bonus', 10, 10, ?, NULL, NULL, 'system', '', '{}', ?)`),
				idArg(driver, ledgerID), idArg(driver, appID), idArg(driver, userID),
				"signup:"+userID.String(), nowArg)
			if err != nil {
				t.Fatalf("insert ledger: %v", err)
			}

			_, err = database.ExecContext(ctx, q(`UPDATE credit_ledger SET amount = 1 WHERE id = ?`), idArg(driver, ledgerID))
			if err == nil {
				t.Fatal("expected UPDATE on credit_ledger to fail")
			}
			_, err = database.ExecContext(ctx, q(`DELETE FROM credit_ledger WHERE id = ?`), idArg(driver, ledgerID))
			if err == nil {
				t.Fatal("expected DELETE on credit_ledger to fail")
			}
		})
	}
}

func idArg(driver string, id uuid.UUID) any {
	if driver == "sqlite" {
		return id.String()
	}
	return id
}

func postgresDSN() string {
	if v := os.Getenv("BUILTLY_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://builtly:builtly@127.0.0.1:5432/builtly_test?sslmode=disable"
}
