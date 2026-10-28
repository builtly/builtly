package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/outbox"
)

// PurgePayload is published on user.deleted for storage.purge.
type PurgePayload struct {
	UserID string `json:"user_id"`
}

// PurgeSubscriber deletes R2 objects for files marked deleted on account deletion.
type PurgeSubscriber struct {
	svc *Service
	log *slog.Logger
}

// NewPurgeSubscriber constructs a PurgeSubscriber.
func NewPurgeSubscriber(svc *Service, log *slog.Logger) *PurgeSubscriber {
	if log == nil {
		log = slog.Default()
	}
	return &PurgeSubscriber{svc: svc, log: log}
}

// Handle is an outbox.Handler.
func (p *PurgeSubscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: fmt.Errorf("storage.purge: missing app_id")}
	}
	var payload PurgePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	userID, err := uuid.Parse(payload.UserID)
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	files, err := p.svc.store.ListDeleted(ctx, nil, *row.AppID, userID, 500)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	cfg, err := p.svc.r2Config(ctx, *row.AppID)
	if err == ErrIntegrationNotConfigured {
		return outbox.ErrNotConfigured
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	var last error
	for _, f := range files {
		if err := p.svc.r2.DeleteObject(ctx, cfg.R2Config, f.ObjectKey); err != nil {
			p.log.Error("storage purge delete failed", "file_id", f.ID, "err", err)
			last = err
			continue
		}
		_ = p.svc.store.HardDelete(ctx, nil, f.AppID, f.ID)
	}
	if last != nil {
		return outbox.ErrRetryable{Err: last}
	}
	return nil
}
