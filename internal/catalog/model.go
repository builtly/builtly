package catalog

import (
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Product is a credit pack in an app catalog.
type Product struct {
	ID              uuid.UUID `db:"id" json:"id"`
	AppID           uuid.UUID `db:"app_id" json:"app_id"`
	SKU             string    `db:"sku" json:"sku"`
	Name            string    `db:"name" json:"name"`
	Description     string    `db:"description" json:"description"`
	Kind            string    `db:"kind" json:"kind"`
	Credits         int64     `db:"credits" json:"credits"`
	PriceAmount     int64     `db:"price_amount" json:"price_amount"`
	Currency        string    `db:"currency" json:"currency"`
	StripePriceID   *string   `db:"stripe_price_id" json:"stripe_price_id,omitempty"`
	AppleProductID  *string   `db:"apple_product_id" json:"apple_product_id,omitempty"`
	GoogleProductID *string   `db:"google_product_id" json:"google_product_id,omitempty"`
	Active          bool      `db:"active" json:"active"`
	SortOrder       int       `db:"sort_order" json:"sort_order"`
	Metadata        []byte    `db:"metadata" json:"metadata"`
	CreatedAt       db.Time   `db:"created_at" json:"created_at"`
	UpdatedAt       db.Time   `db:"updated_at" json:"updated_at"`
}

const KindCreditPack = "credit_pack"
