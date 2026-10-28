package billing

import (
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

const (
	ProviderStripe = "stripe"
	ProviderApple  = "apple"
	ProviderGoogle = "google"

	StatusPending         = "pending"
	StatusCompleted       = "completed"
	StatusPartialRefunded = "partially_refunded"
	StatusRefunded        = "refunded"
	StatusFailed          = "failed"
	StatusExpired         = "expired"

	EnvProduction = "production"
	EnvSandbox    = "sandbox"

	WebhookReceived  = "received"
	WebhookProcessed = "processed"
	WebhookIgnored   = "ignored"
	WebhookFailed    = "failed"
)

// Purchase is a credit pack purchase.
type Purchase struct {
	ID              uuid.UUID   `db:"id" json:"id"`
	AppID           uuid.UUID   `db:"app_id" json:"app_id"`
	UserID          uuid.UUID   `db:"user_id" json:"user_id"`
	ProductID       uuid.UUID   `db:"product_id" json:"product_id"`
	Provider        string      `db:"provider" json:"provider"`
	ConnectionID    string      `db:"connection_id" json:"connection_id"`
	ProviderRef     *string     `db:"provider_ref" json:"provider_ref,omitempty"`
	PaymentRef      *string     `db:"payment_ref" json:"payment_ref,omitempty"`
	Status          string      `db:"status" json:"status"`
	Credits         int64       `db:"credits" json:"credits"`
	RefundedCredits int64       `db:"refunded_credits" json:"refunded_credits"`
	Amount          *int64      `db:"amount" json:"amount,omitempty"`
	Currency        *string     `db:"currency" json:"currency,omitempty"`
	Environment     string      `db:"environment" json:"environment"`
	AffiliateRef    *string     `db:"affiliate_ref" json:"affiliate_ref,omitempty"`
	ProviderData    []byte      `db:"provider_data" json:"-"`
	CreatedAt       db.Time     `db:"created_at" json:"created_at"`
	UpdatedAt       db.Time     `db:"updated_at" json:"updated_at"`
	CompletedAt     db.NullTime `db:"completed_at" json:"completed_at,omitempty"`
}

// WebhookEvent is a deduped inbound webhook.
type WebhookEvent struct {
	ID           uuid.UUID   `db:"id"`
	Provider     string      `db:"provider"`
	ConnectionID string      `db:"connection_id"`
	EventID      string      `db:"event_id"`
	EventType    string      `db:"event_type"`
	AppID        *uuid.UUID  `db:"app_id"`
	Status       string      `db:"status"`
	Attempts     int         `db:"attempts"`
	LastError    string      `db:"last_error"`
	ReceivedAt   db.Time     `db:"received_at"`
	ProcessedAt  db.NullTime `db:"processed_at"`
}
