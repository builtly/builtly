package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
)

func TestDefaultSubscriptions(t *testing.T) {
	reg := outbox.NewRegistry()
	reg.DefaultSubscriptions()
	subs := reg.SubscribersFor(outbox.EventPurchaseCompleted)
	want := map[string]bool{outbox.SubAnalytics: true, outbox.SubAffiliate: true, outbox.SubReferral: true}
	if len(subs) != len(want) {
		t.Fatalf("%v", subs)
	}
	for _, s := range subs {
		if !want[s] {
			t.Fatalf("unexpected %s", s)
		}
	}
}

func TestErrTypes(t *testing.T) {
	inner := errors.New("boom")
	r := outbox.ErrRetryable{Err: inner}
	if r.Error() != "boom" || !errors.Is(r, inner) {
		t.Fatalf("%v", r)
	}
	p := outbox.ErrPermanent{Err: inner}
	if p.Error() != "boom" || !errors.Is(p, inner) {
		t.Fatalf("%v", p)
	}
}

func TestDispatcherRunIdleCancel(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestOutboxListAndRetryDead(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box := testBox(t)
	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, func(ctx context.Context, row outbox.Row, payload []byte) error {
		return outbox.ErrPermanent{Err: errors.New("nope")}
	})
	pub := outbox.NewPublisher(store, box, reg)
	appID := uuid.Must(uuid.NewV7())
	if err := pub.Publish(context.Background(), database, outbox.Event{
		AppID: &appID, Type: outbox.EventEmailRequested, Payload: []byte(`{"x":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	d := outbox.NewDispatcher(store, box, reg, "inst", discardLog())
	if _, err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.List(context.Background(), "dead", outbox.SubEmail, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list dead: %v n=%d", err, len(rows))
	}
	if err := store.RetryDead(context.Background(), rows[0].ID, db.Now()); err != nil {
		t.Fatal(err)
	}
}
