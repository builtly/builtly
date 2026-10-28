package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/platform/httpx"
)

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.catalog.List(r.Context(), appID, false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		SKU             string          `json:"sku"`
		Name            string          `json:"name"`
		Description     string          `json:"description"`
		Credits         int64           `json:"credits"`
		PriceAmount     int64           `json:"price_amount"`
		Currency        string          `json:"currency"`
		StripePriceID   *string         `json:"stripe_price_id"`
		AppleProductID  *string         `json:"apple_product_id"`
		GoogleProductID *string         `json:"google_product_id"`
		Active          *bool           `json:"active"`
		SortOrder       *int            `json:"sort_order"`
		Metadata        json.RawMessage `json:"metadata"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.catalog.Create(r.Context(), appID, catalog.CreateInput{
		SKU: body.SKU, Name: body.Name, Description: body.Description,
		Credits: body.Credits, PriceAmount: body.PriceAmount, Currency: body.Currency,
		StripePriceID: body.StripePriceID, AppleProductID: body.AppleProductID, GoogleProductID: body.GoogleProductID,
		Active: body.Active, SortOrder: body.SortOrder, Metadata: body.Metadata,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "product.create", "product", p.ID.String(), map[string]any{"sku": p.SKU})
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (h *Handler) patchProduct(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "product_id"))
	if err != nil {
		httpx.WriteError(w, r, catalog.ErrProductNotFound)
		return
	}
	var body map[string]json.RawMessage
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := catalog.PatchInput{}
	if v, ok := body["sku"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		in.SKU = &s
	}
	if v, ok := body["name"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		in.Name = &s
	}
	if v, ok := body["description"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		in.Description = &s
	}
	if v, ok := body["credits"]; ok {
		var n int64
		_ = json.Unmarshal(v, &n)
		in.Credits = &n
	}
	if v, ok := body["price_amount"]; ok {
		var n int64
		_ = json.Unmarshal(v, &n)
		in.PriceAmount = &n
	}
	if v, ok := body["currency"]; ok {
		var s string
		_ = json.Unmarshal(v, &s)
		in.Currency = &s
	}
	if v, ok := body["stripe_price_id"]; ok {
		if string(v) == "null" {
			in.ClearStripe = true
		} else {
			var s string
			_ = json.Unmarshal(v, &s)
			in.StripePriceID = &s
		}
	}
	if v, ok := body["apple_product_id"]; ok {
		if string(v) == "null" {
			in.ClearApple = true
		} else {
			var s string
			_ = json.Unmarshal(v, &s)
			in.AppleProductID = &s
		}
	}
	if v, ok := body["google_product_id"]; ok {
		if string(v) == "null" {
			in.ClearGoogle = true
		} else {
			var s string
			_ = json.Unmarshal(v, &s)
			in.GoogleProductID = &s
		}
	}
	if v, ok := body["active"]; ok {
		var b bool
		_ = json.Unmarshal(v, &b)
		in.Active = &b
	}
	if v, ok := body["sort_order"]; ok {
		var n int
		_ = json.Unmarshal(v, &n)
		in.SortOrder = &n
	}
	if v, ok := body["metadata"]; ok {
		in.Metadata = json.RawMessage(v)
	}
	p, err := h.catalog.Patch(r.Context(), appID, pid, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "product.update", "product", p.ID.String(), map[string]any{"sku": p.SKU})
	httpx.WriteJSON(w, http.StatusOK, p)
}
