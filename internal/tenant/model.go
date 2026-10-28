package tenant

import (
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Status values for apps.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// App is a tenant.
type App struct {
	ID          uuid.UUID `db:"id" json:"id"`
	Slug        string    `db:"slug" json:"slug"`
	Name        string    `db:"name" json:"name"`
	Status      string    `db:"status" json:"status"`
	Settings    Settings  `db:"-" json:"settings"`
	RawSettings []byte    `db:"settings" json:"-"`
	CreatedAt   db.Time   `db:"created_at" json:"created_at"`
	UpdatedAt   db.Time   `db:"updated_at" json:"updated_at"`
}

// APIKey is an app-scoped machine credential.
type APIKey struct {
	ID         uuid.UUID   `db:"id" json:"id"`
	AppID      uuid.UUID   `db:"app_id" json:"app_id"`
	Name       string      `db:"name" json:"name"`
	Prefix     string      `db:"prefix" json:"prefix"`
	KeyHash    []byte      `db:"key_hash" json:"-"`
	Scopes     []byte      `db:"scopes" json:"scopes"`
	LastUsedAt db.NullTime `db:"last_used_at" json:"last_used_at,omitempty"`
	RevokedAt  db.NullTime `db:"revoked_at" json:"revoked_at,omitempty"`
	CreatedAt  db.Time     `db:"created_at" json:"created_at"`
}

// DefaultScopes for newly created API keys.
var DefaultScopes = []string{"credits:consume"}
