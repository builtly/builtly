package credit

import (
	"encoding/json"
	"net/http"
	"strconv"

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
	items, err := h.svc.ListLedger(r.Context(), app.ID, p.UserID, limit)
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nil})
}
