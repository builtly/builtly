package credit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// UserHandler serves /user/credits* endpoints.
type UserHandler struct {
	svc *Service
}

// NewUserHandler constructs user-facing credit handlers.
func NewUserHandler(svc *Service) *UserHandler { return &UserHandler{svc: svc} }

// GetBalance handles GET /user/credits.
func (h *UserHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	p, _ := principal.FromContext(r.Context())
	b, err := h.svc.GetBalance(r.Context(), app.ID, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"balance":    b.Balance,
		"updated_at": b.UpdatedAt,
	})
}

// GetLedger handles GET /user/credits/ledger.
func (h *UserHandler) GetLedger(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	p, _ := principal.FromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	afterT, afterID, err := httpx.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid cursor"}))
		return
	}
	items, err := h.svc.ListLedgerPaged(r.Context(), app.ID, p.UserID, limit, db.Time(afterT), afterID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, e := range items {
		out = append(out, map[string]any{
			"id": e.ID, "entry_type": e.EntryType, "amount": e.Amount,
			"balance_after": e.BalanceAfter, "reference_type": e.ReferenceType,
			"reference_id": e.ReferenceID, "metadata": json.RawMessage(e.Metadata),
			"created_at": e.CreatedAt,
		})
	}
	var nextCursor any
	if len(items) == limit && limit > 0 {
		last := items[len(items)-1]
		nextCursor = httpx.EncodeCursor(time.Time(last.CreatedAt), last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nextCursor})
}
