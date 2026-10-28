package storage

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists file rows.
type Store struct{ db *db.DB }

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

// Insert creates a pending file row.
func (s *Store) Insert(ctx context.Context, q db.Querier, f File) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO files (
			id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
			size_bytes, status, created_at, uploaded_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		f.ID, f.AppID, f.UserID, f.ConnectionID, f.Bucket, f.ObjectKey, f.Filename, f.ContentType,
		f.SizeBytes, f.Status, f.CreatedAt, f.UploadedAt, f.DeletedAt)
	return db.MapError(err)
}

// GetByID loads a file for an app+user.
func (s *Store) GetByID(ctx context.Context, q db.Querier, appID, userID, fileID uuid.UUID) (File, error) {
	var f File
	err := s.q(q).GetContext(ctx, &f, s.db.Rebind(`
		SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
		       size_bytes, status, created_at, uploaded_at, deleted_at
		FROM files WHERE app_id = ? AND user_id = ? AND id = ?`), appID, userID, fileID)
	return f, db.MapError(err)
}

// GetByIDAnyUser loads by app+id (admin / purge).
func (s *Store) GetByIDAnyUser(ctx context.Context, q db.Querier, appID, fileID uuid.UUID) (File, error) {
	var f File
	err := s.q(q).GetContext(ctx, &f, s.db.Rebind(`
		SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
		       size_bytes, status, created_at, uploaded_at, deleted_at
		FROM files WHERE app_id = ? AND id = ?`), appID, fileID)
	return f, db.MapError(err)
}

// ListUploaded lists uploaded files for a user (newest first).
func (s *Store) ListUploaded(ctx context.Context, q db.Querier, appID, userID uuid.UUID, limit int, after db.Time, afterID uuid.UUID) ([]File, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows []File
	var err error
	if afterID == uuid.Nil {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
			       size_bytes, status, created_at, uploaded_at, deleted_at
			FROM files WHERE app_id = ? AND user_id = ? AND status = ?
			ORDER BY created_at DESC, id DESC LIMIT ?`), appID, userID, StatusUploaded, limit)
	} else {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
			SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
			       size_bytes, status, created_at, uploaded_at, deleted_at
			FROM files WHERE app_id = ? AND user_id = ? AND status = ?
			  AND (created_at < ? OR (created_at = ? AND id < ?))
			ORDER BY created_at DESC, id DESC LIMIT ?`),
			appID, userID, StatusUploaded, after, after, afterID, limit)
	}
	return rows, db.MapError(err)
}

// SumUsageBytes sums pending+uploaded size for quota.
func (s *Store) SumUsageBytes(ctx context.Context, q db.Querier, appID, userID uuid.UUID) (int64, error) {
	var n int64
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`
		SELECT COALESCE(SUM(size_bytes), 0) FROM files
		WHERE app_id = ? AND user_id = ? AND status IN (?, ?)`),
		appID, userID, StatusPending, StatusUploaded)
	return n, db.MapError(err)
}

// MarkUploaded sets status uploaded.
func (s *Store) MarkUploaded(ctx context.Context, q db.Querier, appID, fileID uuid.UUID, at db.Time) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE files SET status = ?, uploaded_at = ? WHERE app_id = ? AND id = ? AND status = ?`),
		StatusUploaded, at, appID, fileID, StatusPending)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// MarkDeleted soft-deletes one file.
func (s *Store) MarkDeleted(ctx context.Context, q db.Querier, appID, fileID uuid.UUID, at db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE files SET status = ?, deleted_at = ? WHERE app_id = ? AND id = ? AND status != ?`),
		StatusDeleted, at, appID, fileID, StatusDeleted)
	return db.MapError(err)
}

// MarkDeletedForUser marks all non-deleted files for a user (account deletion).
func (s *Store) MarkDeletedForUser(ctx context.Context, q db.Querier, appID, userID uuid.UUID, at db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE files SET status = ?, deleted_at = ? WHERE app_id = ? AND user_id = ? AND status != ?`),
		StatusDeleted, at, appID, userID, StatusDeleted)
	return db.MapError(err)
}

// HardDelete removes the row.
func (s *Store) HardDelete(ctx context.Context, q db.Querier, appID, fileID uuid.UUID) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`DELETE FROM files WHERE app_id = ? AND id = ?`), appID, fileID)
	return db.MapError(err)
}

// ListPendingOlderThan returns pending files older than cutoff.
func (s *Store) ListPendingOlderThan(ctx context.Context, q db.Querier, cutoff time.Time, limit int) ([]File, error) {
	var rows []File
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
		       size_bytes, status, created_at, uploaded_at, deleted_at
		FROM files WHERE status = ? AND created_at < ? ORDER BY created_at ASC LIMIT ?`),
		StatusPending, db.Time(cutoff.UTC()), limit)
	return rows, db.MapError(err)
}

// ListDeleted returns deleted files still holding object keys (for purge retry).
func (s *Store) ListDeleted(ctx context.Context, q db.Querier, appID, userID uuid.UUID, limit int) ([]File, error) {
	var rows []File
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
		       size_bytes, status, created_at, uploaded_at, deleted_at
		FROM files WHERE app_id = ? AND user_id = ? AND status = ? ORDER BY deleted_at ASC LIMIT ?`),
		appID, userID, StatusDeleted, limit)
	return rows, db.MapError(err)
}

// ListDeletedGlobal lists deleted files across apps for cleanup retries.
func (s *Store) ListDeletedGlobal(ctx context.Context, q db.Querier, limit int) ([]File, error) {
	var rows []File
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, user_id, connection_id, bucket, object_key, filename, content_type,
		       size_bytes, status, created_at, uploaded_at, deleted_at
		FROM files WHERE status = ? ORDER BY deleted_at ASC LIMIT ?`),
		StatusDeleted, limit)
	return rows, db.MapError(err)
}
