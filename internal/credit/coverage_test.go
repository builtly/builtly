package credit_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
)

func creditDrivers(t *testing.T) []string {
	t.Helper()
	drivers := []string{"sqlite"}
	if os.Getenv("BUILTLY_TEST_DATABASE_URL") != "" || os.Getenv("BUILTLY_TEST_POSTGRES") == "1" {
		if probe, err := db.Open("postgres", postgresDSN(), 2); err == nil {
			_ = probe.Close()
			drivers = append(drivers, "postgres")
		}
	}
	return drivers
}

func postgresDSN() string {
	if v := os.Getenv("BUILTLY_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://builtly:builtly@127.0.0.1:5432/builtly_test?sslmode=disable"
}

func TestConsumeRaceBothDialects(t *testing.T) {
	for _, driver := range creditDrivers(t) {
		t.Run(driver, func(t *testing.T) {
			database := dbtest.New(t, driver)
			svc := credit.NewService(database, credit.NewStore(database))
			appID, userID := seedAppUser(t, database)
			ctx := context.Background()
			if _, err := svc.Apply(ctx, credit.ApplyInput{
				AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 10,
				IdempotencyKey: "admin:seed-" + driver, Actor: "admin", Reason: "seed",
			}); err != nil {
				t.Fatal(err)
			}
			var okCount atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := svc.Consume(ctx, appID, userID, 1, "race-"+uuid.Must(uuid.NewV7()).String(), "chat", "api_key:x", nil)
					if err == nil {
						okCount.Add(1)
					}
				}()
			}
			wg.Wait()
			if okCount.Load() != 10 {
				t.Fatalf("want 10 successes, got %d", okCount.Load())
			}
			bal, err := svc.GetBalance(ctx, appID, userID)
			if err != nil || bal.Balance != 0 {
				t.Fatalf("balance=%v err=%v", bal, err)
			}
			mismatches, err := svc.Reconcile(ctx, &appID)
			if err != nil || len(mismatches) != 0 {
				t.Fatalf("reconcile %v %v", mismatches, err)
			}
		})
	}
}

func TestClearDebtConcurrentRefund(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()

	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: -20,
		IdempotencyKey: "admin:debt-start", Actor: "admin", Reason: "seed debt",
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var clearErrs, refundErrs atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, err := svc.ClearDebt(ctx, appID, userID, "clear", "audit-clear-"+uuid.Must(uuid.NewV7()).String())
			if err != nil {
				clearErrs.Add(1)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			_, err := svc.Apply(ctx, credit.ApplyInput{
				AppID: appID, UserID: userID, EntryType: credit.TypeRefund, Amount: -5,
				IdempotencyKey: "refund-concurrent-" + uuid.Must(uuid.NewV7()).String(),
				Actor:          "system", Reason: "concurrent refund",
			})
			if err != nil {
				refundErrs.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if clearErrs.Load() != 0 {
		t.Fatalf("clear debt errors: %d", clearErrs.Load())
	}
	bal, err := svc.GetBalance(ctx, appID, userID)
	if err != nil {
		t.Fatal(err)
	}
	// After clears and refunds: final balance is either 0 (cleared last) or negative (refund after clear).
	if bal.Balance > 0 {
		t.Fatalf("unexpected positive balance %d", bal.Balance)
	}
	mismatches, err := svc.Reconcile(ctx, &appID)
	if err != nil || len(mismatches) != 0 {
		t.Fatalf("reconcile after concurrent clear/refund: %v %v bal=%d", mismatches, err, bal.Balance)
	}
}

func TestReconcileRandomized(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()

	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeSignupBonus, Amount: 100,
		IdempotencyKey: "signup:" + userID.String(), Actor: "system",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		key := "op-" + uuid.Must(uuid.NewV7()).String()
		switch i % 3 {
		case 0:
			_, _ = svc.Consume(ctx, appID, userID, 1, key, "feat", "user", nil)
		case 1:
			_, _ = svc.Apply(ctx, credit.ApplyInput{
				AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 3,
				IdempotencyKey: key, Actor: "admin", Reason: "topup",
			})
		default:
			_, _ = svc.Apply(ctx, credit.ApplyInput{
				AppID: appID, UserID: userID, EntryType: credit.TypeRefund, Amount: -2,
				IdempotencyKey: key, Actor: "system", Reason: "partial",
			})
		}
	}
	items, err := svc.ListLedger(ctx, appID, userID, 50)
	if err != nil || len(items) < 2 {
		t.Fatalf("ledger: %v n=%d", err, len(items))
	}
	mismatches, err := svc.Reconcile(ctx, &appID)
	if err != nil || len(mismatches) != 0 {
		t.Fatalf("reconcile: %v %v", mismatches, err)
	}
}

func TestApplyReferenceHelpers(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()
	purchaseID := uuid.Must(uuid.NewV7()).String()
	res, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAffiliatePayout, Amount: 40,
		IdempotencyKey: "affiliate:purchase:" + purchaseID,
		ReferenceType:  "purchase", ReferenceID: purchaseID,
		Actor: "system", Reason: "referral",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetByReference(ctx, appID, credit.TypeAffiliatePayout, "purchase", purchaseID)
	if err != nil || got.ID != res.Entry.ID {
		t.Fatalf("GetByReference: %+v err=%v", got, err)
	}
	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAffiliateReversal, Amount: -10,
		IdempotencyKey: "affiliate_reversal:" + purchaseID + ":10",
		ReferenceType:  "purchase", ReferenceID: purchaseID,
		Actor: "system", Reason: "reversal",
	}); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.SumReversals(ctx, appID, "purchase", purchaseID)
	if err != nil || sum != 10 {
		t.Fatalf("SumReversals=%d err=%v", sum, err)
	}
	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeSignupBonus, Amount: -1,
		IdempotencyKey: "bad-sign", Actor: "system",
	}); err != credit.ErrInvalidAmount {
		t.Fatalf("want invalid amount, got %v", err)
	}
}
