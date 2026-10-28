package storage

import (
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

const (
	StatusPending  = "pending"
	StatusUploaded = "uploaded"
	StatusDeleted  = "deleted"
)

// File is a user-owned object metadata row.
type File struct {
	ID           uuid.UUID   `db:"id" json:"id"`
	AppID        uuid.UUID   `db:"app_id" json:"-"`
	UserID       uuid.UUID   `db:"user_id" json:"-"`
	ConnectionID string      `db:"connection_id" json:"-"`
	Bucket       string      `db:"bucket" json:"-"`
	ObjectKey    string      `db:"object_key" json:"-"`
	Filename     string      `db:"filename" json:"filename"`
	ContentType  string      `db:"content_type" json:"content_type"`
	SizeBytes    int64       `db:"size_bytes" json:"size_bytes"`
	Status       string      `db:"status" json:"status"`
	CreatedAt    db.Time     `db:"created_at" json:"created_at"`
	UploadedAt   db.NullTime `db:"uploaded_at" json:"uploaded_at,omitempty"`
	DeletedAt    db.NullTime `db:"deleted_at" json:"deleted_at,omitempty"`
}

// PublicFile is the API shape for a file.
func (f File) Public() map[string]any {
	m := map[string]any{
		"id": f.ID.String(), "filename": f.Filename, "content_type": f.ContentType,
		"size_bytes": f.SizeBytes, "status": f.Status, "created_at": f.CreatedAt,
	}
	if f.UploadedAt.Valid {
		m["uploaded_at"] = f.UploadedAt.Time
	}
	return m
}
