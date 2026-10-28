package credit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/tenant"
)

func seedAppUser(t *testing.T, database *db.DB) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(ctx, "app-"+uuid.Must(uuid.NewV7()).String()[:8], "App", tenant.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	email := "u@example.com"
	code := "ABCD1234"
	u := auth.User{
		ID: uuid.Must(uuid.NewV7()), AppID: app.ID, Email: &email,
		DisplayName: "U", Status: auth.StatusActive, ReferralCode: &code,
		CreatedAt: db.Now(), UpdatedAt: db.Now(),
	}
	if err := auth.NewStore(database).Insert(ctx, nil, u); err != nil {
		t.Fatal(err)
	}
	return app.ID, u.ID
}

func TestConsumeRace(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()

	_, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 10,
		IdempotencyKey: "admin:seed", Actor: "admin", Reason: "seed",
	})
	if err != nil {
		t.Fatal(err)
	}

	var okCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.Consume(ctx, appID, userID, 1, "race-"+uuid.Must(uuid.NewV7()).String(), "chat", "api_key:x", nil)
			if err == nil {
				okCount.Add(1)
			}
		}(i)
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
}

func TestConsumeIdempotency(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()
	_, _ = svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 100,
		IdempotencyKey: "admin:seed2", Actor: "admin", Reason: "seed",
	})

	var wg sync.WaitGroup
	results := make([]credit.ConsumeResult, 20)
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Consume(ctx, appID, userID, 3, "same-key", "feat", "user:x", nil)
		}(i)
	}
	wg.Wait()
	var first credit.ConsumeResult
	ok := 0
	for i := 0; i < 20; i++ {
		if errs[i] != nil {
			t.Fatalf("unexpected err: %v", errs[i])
		}
		if ok == 0 {
			first = results[i]
		}
		if results[i].Entry.ID != first.Entry.ID || results[i].Balance != first.Balance {
			t.Fatalf("divergent responses: %+v vs %+v", first, results[i])
		}
		ok++
	}
	bal, _ := svc.GetBalance(ctx, appID, userID)
	if bal.Balance != 97 {
		t.Fatalf("balance %d", bal.Balance)
	}

	_, err := svc.Consume(ctx, appID, userID, 4, "same-key", "feat", "user:x", nil)
	if err != credit.ErrIdempotencyConflict {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestClearDebt(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()

	_, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: -35,
		IdempotencyKey: "admin:debt", Actor: "admin", Reason: "clawback sim",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.ClearDebt(ctx, appID, userID, "debt cleared", uuid.Must(uuid.NewV7()).String())
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 35 || res.Balance != 0 || res.LedgerEntry == nil {
		t.Fatalf("%+v", res)
	}
	res2, err := svc.ClearDebt(ctx, appID, userID, "", uuid.Must(uuid.NewV7()).String())
	if err != nil {
		t.Fatal(err)
	}
	if res2.Cleared != 0 || res2.LedgerEntry != nil {
		t.Fatalf("noop expected: %+v", res2)
	}
}
