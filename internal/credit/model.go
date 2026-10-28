package credit

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Entry types.
const (
	TypeSignupBonus       = "signup_bonus"
	TypeReferralBonus     = "referral_bonus"
	TypeStripePurchase    = "stripe_purchase"
	TypeIAPPurchase       = "iap_purchase"
	TypeAIConsume         = "ai_consume"
	TypeRefund            = "refund"
	TypeRefundReversal    = "refund_reversal"
	TypeAffiliatePayout   = "affiliate_payout"
	TypeAffiliateReversal = "affiliate_reversal"
	TypeAdminAdjust       = "admin_adjust"
)

// Balance is the current credit balance for a user in an app.
type Balance struct {
	AppID     uuid.UUID `db:"app_id" json:"app_id"`
	UserID    uuid.UUID `db:"user_id" json:"user_id"`
	Balance   int64     `db:"balance" json:"balance"`
	UpdatedAt db.Time   `db:"updated_at" json:"updated_at"`
}

// Entry is one immutable ledger row.
type Entry struct {
	ID             uuid.UUID `db:"id" json:"id"`
	AppID          uuid.UUID `db:"app_id" json:"app_id"`
	UserID         uuid.UUID `db:"user_id" json:"user_id"`
	EntryType      string    `db:"entry_type" json:"entry_type"`
	Amount         int64     `db:"amount" json:"amount"`
	BalanceAfter   int64     `db:"balance_after" json:"balance_after"`
	IdempotencyKey string    `db:"idempotency_key" json:"idempotency_key"`
	ReferenceType  *string   `db:"reference_type" json:"reference_type,omitempty"`
	ReferenceID    *string   `db:"reference_id" json:"reference_id,omitempty"`
	Actor          string    `db:"actor" json:"actor"`
	Reason         string    `db:"reason" json:"reason"`
	Metadata       []byte    `db:"metadata" json:"metadata"`
	CreatedAt      db.Time   `db:"created_at" json:"created_at"`
}

// ApplyInput is an unconditional ledger write (not ai_consume).
type ApplyInput struct {
	AppID          uuid.UUID
	UserID         uuid.UUID
	EntryType      string
	Amount         int64 // signed
	IdempotencyKey string
	ReferenceType  string
	ReferenceID    string
	Actor          string
	Reason         string
	Metadata       json.RawMessage
}

// ConsumeResult is returned by Consume.
type ConsumeResult struct {
	Entry    Entry
	Balance  int64
	Replayed bool
}

// ApplyResult is returned by ApplyTx.
type ApplyResult struct {
	Entry   Entry
	Balance int64
	Applied bool // false if idempotent replay
}

// ClearDebtResult is returned by ClearDebt.
type ClearDebtResult struct {
	Cleared     int64  `json:"cleared"`
	Balance     int64  `json:"balance"`
	LedgerEntry *Entry `json:"ledger_entry"`
}
