package outbox

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/secrets"
)

const maxAttempts = 12

// Dispatcher claims and delivers outbox rows.
type Dispatcher struct {
	store      *Store
	box        *secrets.Box
	registry   *Registry
	instanceID string
	log        *slog.Logger
	idleWait   time.Duration
}

// NewDispatcher constructs a Dispatcher.
func NewDispatcher(store *Store, box *secrets.Box, registry *Registry, instanceID string, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store: store, box: box, registry: registry,
		instanceID: instanceID, log: log, idleWait: time.Second,
	}
}

// Run loops until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := d.Tick(ctx)
		if err != nil {
			d.log.Error("outbox tick failed", "err", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.idleWait):
		}
	}
}

// Tick claims and processes at most one row. worked is true if a row was claimed.
func (d *Dispatcher) Tick(ctx context.Context) (worked bool, err error) {
	now := time.Now().UTC()
	row, err := d.store.ClaimDue(ctx, d.instanceID, now)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	d.deliver(ctx, row)
	return true, nil
}

func (d *Dispatcher) deliver(ctx context.Context, row Row) {
	log := d.log.With("outbox_id", row.ID.String(), "subscriber", row.Subscriber, "attempt", row.Attempts, "job", "outbox")
	ctx = logx.With(ctx, log)

	handler, ok := d.registry.HandlerFor(row.Subscriber)
	if !ok {
		_ = d.store.MarkSkipped(ctx, row.ID, db.Now(), "no handler registered")
		return
	}
	aad := []byte(row.ID.String() + row.Subscriber)
	payload, err := d.box.OpenEmbedded(row.PayloadEnc, aad)
	if err != nil {
		_ = d.store.MarkDead(ctx, row.ID, db.Now(), "decrypt: "+err.Error())
		return
	}

	err = handler(ctx, row, payload)
	now := db.Now()
	switch {
	case err == nil:
		_ = d.store.MarkDone(ctx, row.ID, now)
	case errors.Is(err, ErrNotConfigured):
		_ = d.store.MarkSkipped(ctx, row.ID, now, err.Error())
	default:
		var perm ErrPermanent
		if errors.As(err, &perm) || row.Attempts >= maxAttempts {
			_ = d.store.MarkDead(ctx, row.ID, now, err.Error())
			log.Error("outbox dead", "err", err)
			return
		}
		next := backoff(row.Attempts)
		_ = d.store.MarkRetry(ctx, row.ID, db.Time(next), err.Error())
		log.Warn("outbox retry scheduled", "err", err, "next_attempt_at", next)
	}
}

func backoff(attempts int) time.Time {
	// min(30s * 2^(attempts-1), 6h) ±20% jitter
	exp := math.Pow(2, float64(max(attempts-1, 0)))
	sec := 30 * exp
	if sec > 6*3600 {
		sec = 6 * 3600
	}
	jitter := 1 + (rand.Float64()*0.4 - 0.2)
	d := time.Duration(sec*jitter) * time.Second
	return time.Now().UTC().Add(d)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
