package storage

import (
	"context"
	"log/slog"
	"time"
)

// Cleaner removes orphaned pending uploads, retries deleted-object purges, and runs hourly cleanup hooks.
type Cleaner struct {
	svc   *Service
	log   *slog.Logger
	hooks []func(ctx context.Context, now time.Time)
}

// NewCleaner constructs a Cleaner.
func NewCleaner(svc *Service, log *slog.Logger) *Cleaner {
	if log == nil {
		log = slog.Default()
	}
	return &Cleaner{svc: svc, log: log}
}

// AddHook registers an additional idempotent cleanup function to run on every tick.
func (c *Cleaner) AddHook(fn func(ctx context.Context, now time.Time)) {
	if fn != nil {
		c.hooks = append(c.hooks, fn)
	}
}

// Run loops hourly until ctx cancelled.
func (c *Cleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	c.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Tick(ctx)
		}
	}
}

// Tick runs one cleanup pass.
func (c *Cleaner) Tick(ctx context.Context) {
	now := c.svc.now().UTC()
	for _, fn := range c.hooks {
		fn(ctx, now)
	}
	cutoff := now.Add(-24 * time.Hour)
	pending, err := c.svc.store.ListPendingOlderThan(ctx, nil, cutoff, 200)
	if err != nil {
		c.log.Error("storage cleanup list pending", "err", err)
		return
	}
	for _, f := range pending {
		cfg, err := c.svc.r2Config(ctx, f.AppID)
		if err == nil {
			_, _ = c.svc.r2.HeadObject(ctx, cfg.R2Config, f.ObjectKey)
			_ = c.svc.r2.DeleteObject(ctx, cfg.R2Config, f.ObjectKey)
		}
		if err := c.svc.store.HardDelete(ctx, nil, f.AppID, f.ID); err != nil {
			c.log.Error("storage cleanup hard delete pending", "file_id", f.ID, "err", err)
		}
	}

	deleted, err := c.svc.store.ListDeletedGlobal(ctx, nil, 200)
	if err != nil {
		c.log.Error("storage cleanup list deleted", "err", err)
		return
	}
	for _, f := range deleted {
		cfg, err := c.svc.r2Config(ctx, f.AppID)
		if err != nil {
			continue
		}
		if err := c.svc.r2.DeleteObject(ctx, cfg.R2Config, f.ObjectKey); err != nil {
			c.log.Error("storage cleanup purge", "file_id", f.ID, "err", err)
			continue
		}
		_ = c.svc.store.HardDelete(ctx, nil, f.AppID, f.ID)
	}
}
