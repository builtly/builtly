package email

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Variable is one {{{KEY}}} declared on a template. Fallback nil means required at send time.
type Variable struct {
	Key      string           `json:"key"`
	Type     string           `json:"type"`
	Fallback *json.RawMessage `json:"fallback,omitempty"`
}

// Template is per-app email copy. It is not tied to a provider connection.
type Template struct {
	ID        uuid.UUID  `json:"id"`
	AppID     uuid.UUID  `json:"app_id"`
	Key       string     `json:"key"`
	Name      string     `json:"name"`
	Subject   string     `json:"subject"`
	HTML      string     `json:"html"`
	Text      string     `json:"text"`
	Variables []Variable `json:"variables"`
	Published bool       `json:"published"`
	CreatedAt db.Time    `json:"created_at"`
	UpdatedAt db.Time    `json:"updated_at"`
}

// Binding points a system trigger at a published template.
// A missing row means built-in copy (security) or do not send (welcome).
type Binding struct {
	AppID      uuid.UUID `db:"app_id" json:"app_id"`
	Trigger    string    `db:"trigger" json:"trigger"`
	TemplateID uuid.UUID `db:"template_id" json:"template_id"`
	UpdatedAt  db.Time   `db:"updated_at" json:"updated_at"`
}

// Send is an idempotent custom-send record.
type Send struct {
	AppID          uuid.UUID `db:"app_id"`
	IdempotencyKey string    `db:"idempotency_key"`
	TemplateKey    string    `db:"template_key"`
	VariablesHash  string    `db:"variables_hash"`
	OutboxID       uuid.UUID `db:"outbox_id"`
	CreatedAt      db.Time   `db:"created_at"`
}

// Input is a create/update payload.
type Input struct {
	Key       string
	Name      string
	Subject   string
	HTML      string
	Text      string
	Variables []Variable
	Published bool
}
