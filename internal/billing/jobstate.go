package billing

import (
	"context"
	"errors"

	"github.com/builtly/builtly/internal/platform/db"
)

// JobStore persists worker cursors in job_state.
type JobStore struct{ db *db.DB }

// NewJobStore constructs a JobStore.
func NewJobStore(database *db.DB) *JobStore { return &JobStore{db: database} }

// Get returns the value for key, or ("", false, nil) if missing.
func (s *JobStore) Get(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.GetContext(ctx, &v, s.db.Rebind(`SELECT value FROM job_state WHERE key = ?`), key)
	if err != nil {
		if errors.Is(db.MapError(err), db.ErrNotFound) {
			return "", false, nil
		}
		return "", false, db.MapError(err)
	}
	return v, true, nil
}

// Set upserts a job_state row.
func (s *JobStore) Set(ctx context.Context, key, value string) error {
	now := db.Now()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO job_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`),
		key, value, now)
	return db.MapError(err)
}
