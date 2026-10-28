package credit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

func TestUserHandlerBalanceAndLedger(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()
	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 15,
		IdempotencyKey: "admin:handler-seed", Actor: "admin", Reason: "seed",
	}); err != nil {
		t.Fatal(err)
	}

	h := credit.NewUserHandler(svc)
	app := tenant.App{ID: appID, Slug: "app"}
	p := principal.Principal{UserID: userID, Actor: "user:" + userID.String()}

	req := httptest.NewRequest("GET", "/user/credits", nil)
	req = req.WithContext(principal.With(tenant.WithApp(req.Context(), app), p))
	rr := httptest.NewRecorder()
	h.GetBalance(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("balance: %d %s", rr.Code, rr.Body.String())
	}
	var bal map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &bal)
	if int64(bal["balance"].(float64)) != 15 {
		t.Fatalf("balance=%v", bal)
	}

	req = httptest.NewRequest("GET", "/user/credits/ledger?limit=10", nil)
	req = req.WithContext(principal.With(tenant.WithApp(req.Context(), app), p))
	rr = httptest.NewRecorder()
	h.GetLedger(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ledger: %d %s", rr.Code, rr.Body.String())
	}
	var led map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &led)
	items, _ := led["items"].([]any)
	if len(items) < 1 {
		t.Fatalf("items=%v", led)
	}
}

func TestApplyTxAndEdges(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	svc := credit.NewService(database, credit.NewStore(database))
	appID, userID := seedAppUser(t, database)
	ctx := context.Background()

	bal, err := svc.GetBalance(ctx, appID, uuid.Must(uuid.NewV7()))
	if err != nil || bal.Balance != 0 {
		t.Fatalf("missing user balance: %+v err=%v", bal, err)
	}

	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAIConsume, Amount: -1,
		IdempotencyKey: "bad-ai", Actor: "x",
	}); err == nil || err.Error() == "" {
		t.Fatal("ai via Apply expected error")
	}
	if _, err := svc.Apply(ctx, credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: credit.TypeAdminAdjust, Amount: 1,
		IdempotencyKey: "no-reason", Actor: "admin",
	}); err == nil {
		t.Fatal("reason expected")
	}
	if _, err := svc.ApplyTx(ctx, nil, credit.ApplyInput{}); err == nil {
		t.Fatal("ApplyTx nil tx")
	}

	var replayApplied bool
	err = database.WithTx(ctx, func(tx *sqlx.Tx) error {
		res, err := svc.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: appID, UserID: userID, EntryType: credit.TypeSignupBonus, Amount: 7,
			IdempotencyKey: "signup:" + userID.String(), Actor: "system",
		})
		if err != nil {
			return err
		}
		if !res.Applied {
			return errors.New("first apply not applied")
		}
		res2, err := svc.ApplyTx(ctx, tx, credit.ApplyInput{
			AppID: appID, UserID: userID, EntryType: credit.TypeSignupBonus, Amount: 7,
			IdempotencyKey: "signup:" + userID.String(), Actor: "system",
		})
		if err != nil {
			return err
		}
		replayApplied = res2.Applied
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayApplied {
		t.Fatal("idempotent ApplyTx should not re-apply")
	}

	if _, err := svc.Consume(ctx, appID, userID, 0, "k", "f", "a", nil); err != credit.ErrInvalidAmount {
		t.Fatalf("amount: %v", err)
	}
	if _, err := svc.Consume(ctx, appID, userID, 1, "", "f", "a", nil); err != credit.ErrMissingIdempotency {
		t.Fatalf("idem: %v", err)
	}
	_, err = svc.Consume(ctx, appID, userID, 1, "c1", "f", "a", json.RawMessage(`not-json`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Consume(ctx, appID, userID, 1, "c1", "other", "a", nil)
	if err != credit.ErrIdempotencyConflict {
		t.Fatalf("feature conflict: %v", err)
	}
}
