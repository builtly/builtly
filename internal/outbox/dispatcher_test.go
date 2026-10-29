package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
)

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	box, err := secrets.NewBox([]config.KeyMaterial{{
		ID: "1", Key: []byte("0123456789abcdef0123456789abcdef"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOutboxConcurrentClaim(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)

	var deliveries atomic.Int32
	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		deliveries.Add(1)
		mu.Lock()
		seen[row.ID]++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	})

	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	payload, _ := json.Marshal(map[string]string{"template": "verify_email", "to": "a@b.co"})
	if err := pub.Publish(context.Background(), database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	d1 := outbox.NewDispatcher(store, box, reg, "inst-1", discardLog())
	d2 := outbox.NewDispatcher(store, box, reg, "inst-2", discardLog())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = d1.Tick(context.Background())
		}()
		go func() {
			defer wg.Done()
			_, _ = d2.Tick(context.Background())
		}()
	}
	wg.Wait()

	if n := deliveries.Load(); n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("row %s delivered %d times", id, c)
		}
	}
}

func TestOutboxBackoffAndDead(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		return errors.New("transient upstream")
	})
	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	payload, _ := json.Marshal(map[string]string{"to": "a@b.co"})
	ctx := context.Background()
	if err := pub.Publish(ctx, database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	worked, err := d.Tick(ctx)
	if err != nil || !worked {
		t.Fatalf("tick1: worked=%v err=%v", worked, err)
	}
	rows, err := store.List(ctx, outbox.StatusPending, "", 10, db.Time{}, uuid.Nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list pending: %v %#v", err, rows)
	}
	delta := time.Until(rows[0].NextAttempt.Time())
	if delta < 20*time.Second || delta > 45*time.Second {
		t.Fatalf("backoff after attempt 1: delta=%v want ~30s±20%%", delta)
	}

	// Drive remaining attempts until dead (maxAttempts=12).
	for i := 0; i < 11; i++ {
		rows, err = store.List(ctx, outbox.StatusPending, "", 10, db.Time{}, uuid.Nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		if err := store.MarkRetry(ctx, rows[0].ID, db.Time(time.Now().UTC().Add(-time.Second)), "force due"); err != nil {
			t.Fatal(err)
		}
		worked, err = d.Tick(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			t.Fatalf("expected work on loop %d", i)
		}
	}
	dead, err := store.List(ctx, outbox.StatusDead, "", 10, db.Time{}, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 {
		pending, _ := store.List(ctx, outbox.StatusPending, "", 10, db.Time{}, uuid.Nil)
		t.Fatalf("want dead=1, got dead=%d pending=%d", len(dead), len(pending))
	}
	if dead[0].Attempts < 12 {
		t.Fatalf("want attempts>=12, got %d", dead[0].Attempts)
	}
}

func TestOutboxSkippedNotConfigured(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		return outbox.ErrNotConfigured
	})
	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	if err := pub.Publish(context.Background(), database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	worked, err := d.Tick(context.Background())
	if err != nil || !worked {
		t.Fatalf("tick: %v %v", worked, err)
	}
	skipped, err := store.List(context.Background(), outbox.StatusSkipped, "", 10, db.Time{}, uuid.Nil)
	if err != nil || len(skipped) != 1 {
		t.Fatalf("skipped: %v %#v", err, skipped)
	}
}

func TestOutboxPermanentDead(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		return outbox.ErrPermanent{Err: errors.New("bad payload")}
	})
	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	if err := pub.Publish(context.Background(), database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	_, _ = d.Tick(context.Background())
	dead, _ := store.List(context.Background(), outbox.StatusDead, "", 10, db.Time{}, uuid.Nil)
	if len(dead) != 1 || dead[0].Attempts != 1 {
		t.Fatalf("want immediate dead after permanent, got %#v", dead)
	}
}

func TestOutboxRetryDead(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		return outbox.ErrPermanent{Err: errors.New("boom")}
	})
	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	ctx := context.Background()
	if err := pub.Publish(ctx, database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	_, _ = d.Tick(ctx)
	dead, _ := store.List(ctx, outbox.StatusDead, "", 10, db.Time{}, uuid.Nil)
	if err := store.RetryDead(ctx, dead[0].ID, db.Now()); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.List(ctx, outbox.StatusPending, "", 10, db.Time{}, uuid.Nil)
	if len(pending) != 1 || pending[0].Attempts != 0 {
		t.Fatalf("retry dead: %#v", pending)
	}
}
