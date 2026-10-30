package outbox

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// Publisher inserts fan-out rows for an event.
type Publisher struct {
	store    *Store
	box      *secrets.Box
	registry *Registry
}

// NewPublisher constructs a Publisher.
func NewPublisher(store *Store, box *secrets.Box, registry *Registry) *Publisher {
	return &Publisher{store: store, box: box, registry: registry}
}

// PublishTx inserts one outbox row per registered subscriber inside tx.
func (p *Publisher) PublishTx(ctx context.Context, tx *sqlx.Tx, ev Event) error {
	if tx == nil {
		return fmt.Errorf("outbox: PublishTx requires tx")
	}
	subs := p.registry.SubscribersFor(ev.Type)
	if len(subs) == 0 {
		return nil
	}
	now := db.Now()
	for _, sub := range subs {
		id := ev.ID
		if id == uuid.Nil || len(subs) != 1 {
			id = uuid.Must(uuid.NewV7())
		}
		aad := []byte(id.String() + sub)
		enc, err := p.box.SealEmbedded([]byte(ev.Payload), aad)
		if err != nil {
			return err
		}
		row := Row{
			ID: id, AppID: ev.AppID, EventType: ev.Type, Subscriber: sub,
			PayloadEnc: enc, Status: StatusPending, Attempts: 0,
			NextAttempt: now, CreatedAt: now,
		}
		if err := p.store.Insert(ctx, tx, row); err != nil {
			return err
		}
	}
	return nil
}

// Publish opens a transaction and publishes.
func (p *Publisher) Publish(ctx context.Context, database *db.DB, ev Event) error {
	return database.WithTx(ctx, func(tx *sqlx.Tx) error {
		return p.PublishTx(ctx, tx, ev)
	})
}
