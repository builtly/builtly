package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
)

// Service manages the product catalog.
type Service struct {
	store    *Store
	resolver *integration.Resolver
	stripe   *stripeapi.Client
}

// NewService constructs a catalog Service.
func NewService(store *Store, resolver *integration.Resolver, stripe *stripeapi.Client) *Service {
	return &Service{store: store, resolver: resolver, stripe: stripe}
}

// CreateInput is admin product create.
type CreateInput struct {
	SKU             string
	Name            string
	Description     string
	Credits         int64
	PriceAmount     int64
	Currency        string
	StripePriceID   *string
	AppleProductID  *string
	GoogleProductID *string
	Active          *bool
	SortOrder       *int
	Metadata        json.RawMessage
}

// List returns products (activeOnly for public catalog).
func (s *Service) List(ctx context.Context, appID uuid.UUID, activeOnly bool) ([]Product, error) {
	return s.store.List(ctx, nil, appID, activeOnly)
}

// Get returns one product.
func (s *Service) Get(ctx context.Context, appID, id uuid.UUID) (Product, error) {
	p, err := s.store.GetByID(ctx, nil, appID, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Product{}, ErrProductNotFound
		}
		return Product{}, err
	}
	return p, nil
}

// GetByAppleProductID returns an active product mapped to an Apple product id.
func (s *Service) GetByAppleProductID(ctx context.Context, appID uuid.UUID, appleID string) (Product, error) {
	p, err := s.store.GetByAppleProductID(ctx, nil, appID, appleID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Product{}, ErrProductNotAvailable
		}
		return Product{}, err
	}
	if !p.Active {
		return Product{}, ErrProductNotAvailable
	}
	return p, nil
}

// GetByGoogleProductID returns an active product mapped to a Google product id.
func (s *Service) GetByGoogleProductID(ctx context.Context, appID uuid.UUID, googleID string) (Product, error) {
	p, err := s.store.GetByGoogleProductID(ctx, nil, appID, googleID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Product{}, ErrProductNotAvailable
		}
		return Product{}, err
	}
	if !p.Active {
		return Product{}, ErrProductNotAvailable
	}
	return p, nil
}

// Create inserts a product, validating stripe price when set.
func (s *Service) Create(ctx context.Context, appID uuid.UUID, in CreateInput) (Product, error) {
	if err := validateCreate(in); err != nil {
		return Product{}, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	sortOrder := 0
	if in.SortOrder != nil {
		sortOrder = *in.SortOrder
	}
	meta := []byte(in.Metadata)
	if len(meta) == 0 {
		meta = []byte(`{}`)
	}
	now := db.Now()
	p := Product{
		ID: uuid.Must(uuid.NewV7()), AppID: appID, SKU: in.SKU, Name: in.Name, Description: in.Description,
		Kind: KindCreditPack, Credits: in.Credits, PriceAmount: in.PriceAmount,
		Currency: strings.ToLower(in.Currency), StripePriceID: emptyNil(in.StripePriceID),
		AppleProductID: emptyNil(in.AppleProductID), GoogleProductID: emptyNil(in.GoogleProductID),
		Active: active, SortOrder: sortOrder, Metadata: meta, CreatedAt: now, UpdatedAt: now,
	}
	if p.StripePriceID != nil {
		if err := s.validateStripePrice(ctx, appID, *p.StripePriceID, p.Currency); err != nil {
			return Product{}, err
		}
	}
	if err := s.store.Insert(ctx, nil, p); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			return Product{}, ErrSKUTaken
		}
		return Product{}, err
	}
	return p, nil
}

// PatchInput is partial product update.
type PatchInput struct {
	SKU             *string
	Name            *string
	Description     *string
	Credits         *int64
	PriceAmount     *int64
	Currency        *string
	StripePriceID   *string // empty string clears
	AppleProductID  *string
	GoogleProductID *string
	Active          *bool
	SortOrder       *int
	Metadata        json.RawMessage
	ClearStripe     bool
	ClearApple      bool
	ClearGoogle     bool
}

// Patch updates a product.
func (s *Service) Patch(ctx context.Context, appID, id uuid.UUID, in PatchInput) (Product, error) {
	p, err := s.Get(ctx, appID, id)
	if err != nil {
		return Product{}, err
	}
	if in.SKU != nil {
		p.SKU = *in.SKU
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.Credits != nil {
		p.Credits = *in.Credits
	}
	if in.PriceAmount != nil {
		p.PriceAmount = *in.PriceAmount
	}
	if in.Currency != nil {
		p.Currency = strings.ToLower(*in.Currency)
	}
	if in.ClearStripe {
		p.StripePriceID = nil
	} else if in.StripePriceID != nil {
		p.StripePriceID = emptyNil(in.StripePriceID)
	}
	if in.ClearApple {
		p.AppleProductID = nil
	} else if in.AppleProductID != nil {
		p.AppleProductID = emptyNil(in.AppleProductID)
	}
	if in.ClearGoogle {
		p.GoogleProductID = nil
	} else if in.GoogleProductID != nil {
		p.GoogleProductID = emptyNil(in.GoogleProductID)
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	if in.SortOrder != nil {
		p.SortOrder = *in.SortOrder
	}
	if in.Metadata != nil {
		p.Metadata = []byte(in.Metadata)
	}
	p.UpdatedAt = db.Now()
	if p.Credits < 1 || p.PriceAmount < 0 || p.SKU == "" || p.Name == "" || p.Currency == "" {
		return Product{}, ErrValidationFailed
	}
	if p.StripePriceID != nil {
		if err := s.validateStripePrice(ctx, appID, *p.StripePriceID, p.Currency); err != nil {
			return Product{}, err
		}
	}
	if err := s.store.Update(ctx, nil, p); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			return Product{}, ErrSKUTaken
		}
		if errors.Is(err, db.ErrNotFound) {
			return Product{}, ErrProductNotFound
		}
		return Product{}, err
	}
	return p, nil
}

func (s *Service) validateStripePrice(ctx context.Context, appID uuid.UUID, priceID, currency string) error {
	conn, err := s.resolver.For(ctx, appID, integration.ProviderStripe)
	if err == integration.ErrNotConfigured {
		return ErrStripePriceInvalid.WithDetails(map[string]any{"reason": "stripe not configured"})
	}
	if err != nil {
		return err
	}
	var sec integration.StripeSecrets
	if err := json.Unmarshal(conn.Secrets, &sec); err != nil || sec.SecretKey == "" {
		return ErrStripePriceInvalid
	}
	price, err := s.stripe.GetPrice(ctx, sec.SecretKey, priceID)
	if err != nil {
		return ErrStripePriceInvalid.WithDetails(map[string]any{"reason": err.Error()})
	}
	if !price.Active || price.Type != "one_time" || !strings.EqualFold(price.Currency, currency) {
		return ErrStripePriceInvalid.WithDetails(map[string]any{
			"reason": "price must be active one_time with matching currency",
		})
	}
	return nil
}

func validateCreate(in CreateInput) error {
	if in.SKU == "" || in.Name == "" || in.Credits < 1 || in.PriceAmount < 0 || in.Currency == "" {
		return ErrValidationFailed
	}
	return nil
}

func emptyNil(p *string) *string {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(*p) == "" {
		return nil
	}
	s := strings.TrimSpace(*p)
	return &s
}
