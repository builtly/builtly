package catalog

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/tenant"
)

// Handler serves public catalog endpoints.
type Handler struct {
	svc *Service
}

// NewHandler constructs a catalog Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// ListProducts handles GET /products.
func (h *Handler) ListProducts(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	items, err := h.svc.List(r.Context(), app.ID, true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		ch := map[string]any{"stripe": p.StripePriceID != nil}
		if p.AppleProductID != nil {
			ch["apple_product_id"] = *p.AppleProductID
		}
		if p.GoogleProductID != nil {
			ch["google_product_id"] = *p.GoogleProductID
		}
		out = append(out, map[string]any{
			"id": p.ID, "sku": p.SKU, "name": p.Name, "description": p.Description,
			"credits": p.Credits, "price_amount": p.PriceAmount, "currency": p.Currency,
			"channels": ch, "sort_order": p.SortOrder,
		})
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
