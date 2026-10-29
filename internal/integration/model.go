package integration

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Connection is a credential set for one provider.
type Connection struct {
	ID         string  `db:"id" json:"id"`
	Provider   string  `db:"provider" json:"provider"`
	Name       string  `db:"name" json:"name"`
	Settings   []byte  `db:"settings" json:"settings"`
	SecretsEnc []byte  `db:"secrets_enc" json:"-"`
	KeyVersion int     `db:"key_version" json:"-"`
	CreatedAt  db.Time `db:"created_at" json:"created_at"`
	UpdatedAt  db.Time `db:"updated_at" json:"updated_at"`

	// ReadOnly marks env default connections (not stored in DB).
	ReadOnly bool `db:"-" json:"read_only,omitempty"`
}

// Binding is an app ↔ provider connection link.
type Binding struct {
	AppID        uuid.UUID `db:"app_id" json:"app_id"`
	Provider     string    `db:"provider" json:"provider"`
	ConnectionID *string   `db:"connection_id" json:"connection_id"` // nil → env default
	Settings     []byte    `db:"settings" json:"settings"`
	CreatedAt    db.Time   `db:"created_at" json:"created_at"`
	UpdatedAt    db.Time   `db:"updated_at" json:"updated_at"`
}

// Resolved is a decrypted connection ready for use by adapters.
type Resolved struct {
	ConnectionID    string
	Provider        string
	Source          string // binding | default
	Settings        []byte
	Secrets         []byte // plaintext JSON — never log/return
	BindingSettings []byte
}

// PublicConnection is the API-safe view with masked secrets.
type PublicConnection struct {
	ID          string          `json:"id"`
	Provider    string          `json:"provider"`
	Name        string          `json:"name"`
	Settings    json.RawMessage `json:"settings"`
	Secrets     json.RawMessage `json:"secrets"` // masked hints only
	WebhookURL  string          `json:"webhook_url,omitempty"`
	RedirectURL string          `json:"redirect_url,omitempty"`
	UsedByApps  []uuid.UUID     `json:"used_by_apps"`
	ReadOnly    bool            `json:"read_only,omitempty"`
	CreatedAt   db.Time         `json:"created_at"`
	UpdatedAt   db.Time         `json:"updated_at"`
}
