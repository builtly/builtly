package catalog

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists products.
type Store struct{ db *db.DB }

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

type productRow struct {
	ID              uuid.UUID `db:"id"`
	AppID           uuid.UUID `db:"app_id"`
	SKU             string    `db:"sku"`
	Name            string    `db:"name"`
	Description     string    `db:"description"`
	Kind            string    `db:"kind"`
	Credits         int64     `db:"credits"`
	PriceAmount     int64     `db:"price_amount"`
	Currency        string    `db:"currency"`
	StripePriceID   *string   `db:"stripe_price_id"`
	AppleProductID  *string   `db:"apple_product_id"`
	GoogleProductID *string   `db:"google_product_id"`
	Active          int       `db:"active"`
	SortOrder       int       `db:"sort_order"`
	Metadata        []byte    `db:"metadata"`
	CreatedAt       db.Time   `db:"created_at"`
	UpdatedAt       db.Time   `db:"updated_at"`
}

func (r productRow) toProduct() Product {
	return Product{
		ID: r.ID, AppID: r.AppID, SKU: r.SKU, Name: r.Name, Description: r.Description, Kind: r.Kind,
		Credits: r.Credits, PriceAmount: r.PriceAmount, Currency: r.Currency,
		StripePriceID: r.StripePriceID, AppleProductID: r.AppleProductID, GoogleProductID: r.GoogleProductID,
		Active: r.Active != 0, SortOrder: r.SortOrder, Metadata: r.Metadata,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

const selectProduct = `SELECT id, app_id, sku, name, description, kind, credits, price_amount, currency,
	stripe_price_id, apple_product_id, google_product_id, active, sort_order, metadata, created_at, updated_at
	FROM products`

// GetByID loads a product.
func (s *Store) GetByID(ctx context.Context, q db.Querier, appID, id uuid.UUID) (Product, error) {
	var r productRow
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectProduct+` WHERE app_id = ? AND id = ?`), appID, id)
	if err != nil {
		return Product{}, db.MapError(err)
	}
	return r.toProduct(), nil
}

// List returns products for an app.
func (s *Store) List(ctx context.Context, q db.Querier, appID uuid.UUID, activeOnly bool) ([]Product, error) {
	query := selectProduct + ` WHERE app_id = ?`
	args := []any{appID}
	if activeOnly {
		query += ` AND active = ?`
		args = append(args, 1)
	}
	query += ` ORDER BY sort_order, created_at`
	var rows []productRow
	if err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(query), args...); err != nil {
		return nil, db.MapError(err)
	}
	out := make([]Product, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toProduct())
	}
	return out, nil
}

// Insert creates a product.
func (s *Store) Insert(ctx context.Context, q db.Querier, p Product) error {
	active := 0
	if p.Active {
		active = 1
	}
	meta := p.Metadata
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO products (
			id, app_id, sku, name, description, kind, credits, price_amount, currency,
			stripe_price_id, apple_product_id, google_product_id, active, sort_order, metadata, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		p.ID, p.AppID, p.SKU, p.Name, p.Description, p.Kind, p.Credits, p.PriceAmount, p.Currency,
		p.StripePriceID, p.AppleProductID, p.GoogleProductID, active, p.SortOrder, meta, p.CreatedAt, p.UpdatedAt)
	return db.MapError(err)
}

// GetByAppleProductID loads by Apple store product id.
func (s *Store) GetByAppleProductID(ctx context.Context, q db.Querier, appID uuid.UUID, appleID string) (Product, error) {
	var r productRow
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectProduct+` WHERE app_id = ? AND apple_product_id = ?`), appID, appleID)
	if err != nil {
		return Product{}, db.MapError(err)
	}
	return r.toProduct(), nil
}

// GetByGoogleProductID loads by Google Play product id.
func (s *Store) GetByGoogleProductID(ctx context.Context, q db.Querier, appID uuid.UUID, googleID string) (Product, error) {
	var r productRow
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectProduct+` WHERE app_id = ? AND google_product_id = ?`), appID, googleID)
	if err != nil {
		return Product{}, db.MapError(err)
	}
	return r.toProduct(), nil
}

// Update replaces mutable product fields.
func (s *Store) Update(ctx context.Context, q db.Querier, p Product) error {
	active := 0
	if p.Active {
		active = 1
	}
	meta := p.Metadata
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE products SET sku = ?, name = ?, description = ?, credits = ?, price_amount = ?, currency = ?,
			stripe_price_id = ?, apple_product_id = ?, google_product_id = ?, active = ?, sort_order = ?,
			metadata = ?, updated_at = ?
		WHERE app_id = ? AND id = ?`),
		p.SKU, p.Name, p.Description, p.Credits, p.PriceAmount, p.Currency,
		p.StripePriceID, p.AppleProductID, p.GoogleProductID, active, p.SortOrder, meta, p.UpdatedAt,
		p.AppID, p.ID)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}
