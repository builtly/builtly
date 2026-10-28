package storage

import (
	"context"
	"log/slog"
	"time"
)

// Cleaner removes orphaned pending uploads and retries deleted-object purges.
type Cleaner struct {
	svc *Service
	log *slog.Logger
}

// NewCleaner constructs a Cleaner.
func NewCleaner(svc *Service, log *slog.Logger) *Cleaner {
	if log == nil {
		log = slog.Default()
	}
	return &Cleaner{svc: svc, log: log}
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
	cutoff := c.svc.now().UTC().Add(-24 * time.Hour)
	pending, err := c.svc.store.ListPendingOlderThan(ctx, nil, cutoff, 200)
	if err != nil {
		c.log.Error("storage cleanup list pending", "err", err)
		return
	}
	for _, f := range pending {
		cfg, err := c.svc.r2Config(ctx, f.AppID)
		if err == nil {
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
