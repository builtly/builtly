package outbox

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Status values.
const (
	StatusPending = "pending"
	StatusDone    = "done"
	StatusSkipped = "skipped"
	StatusDead    = "dead"
)

// Common event types.
const (
	EventUserRegistered    = "user.registered"
	EventEmailRequested    = "email.requested"
	EventPurchaseCompleted = "purchase.completed"
	EventPurchaseRefunded  = "purchase.refunded"
	EventReferralRewarded  = "referral.rewarded"
	EventCreditsConsumed   = "credits.consumed"
	EventUserDeleted       = "user.deleted"
)

// Subscriber names.
const (
	SubEmail        = "email"
	SubAnalytics    = "analytics"
	SubAffiliate    = "affiliate"
	SubReferral     = "referral"
	SubAppleRevoke  = "auth.apple_revoke"
	SubStoragePurge = "storage.purge"
)

// Row is one outbox delivery unit.
type Row struct {
	ID          uuid.UUID   `db:"id" json:"id"`
	AppID       *uuid.UUID  `db:"app_id" json:"app_id,omitempty"`
	EventType   string      `db:"event_type" json:"event_type"`
	Subscriber  string      `db:"subscriber" json:"subscriber"`
	PayloadEnc  []byte      `db:"payload_enc" json:"-"`
	Status      string      `db:"status" json:"status"`
	Attempts    int         `db:"attempts" json:"attempts"`
	NextAttempt db.Time     `db:"next_attempt_at" json:"next_attempt_at"`
	LockedUntil db.NullTime `db:"locked_until" json:"locked_until,omitempty"`
	LockedBy    *string     `db:"locked_by" json:"locked_by,omitempty"`
	LastError   string      `db:"last_error" json:"last_error"`
	CreatedAt   db.Time     `db:"created_at" json:"created_at"`
	FinishedAt  db.NullTime `db:"finished_at" json:"finished_at,omitempty"`
}

// Event is published by producers inside a transaction.
type Event struct {
	ID      uuid.UUID // optional; used only when the event fans out to one subscriber
	AppID   *uuid.UUID
	Type    string
	Payload json.RawMessage
}
