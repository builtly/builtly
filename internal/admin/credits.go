package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
)

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	users, err := h.users.List(r.Context(), nil, appID, r.URL.Query().Get("q"), r.URL.Query().Get("status"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		bal, _ := h.credits.GetBalance(r.Context(), appID, u.ID)
		items = append(items, map[string]any{
			"id": u.ID, "email": u.Email, "status": u.Status,
			"balance": bal.Balance, "created_at": u.CreatedAt, "last_login_at": nullTime(u.LastLoginAt),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, err := h.users.GetByID(r.Context(), nil, appID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			httpx.WriteError(w, r, auth.ErrUserNotFound)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	bal, err := h.credits.GetBalance(r.Context(), appID, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	purchases := []any{}
	if h.purchases != nil {
		uid := userID
		rows, err := h.purchases.ListAppPurchases(r.Context(), nil, appID, billing.PurchaseListQuery{
			UserID: &uid, Limit: 10,
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		purchases = make([]any, 0, len(rows))
		for _, p := range rows {
			item := map[string]any{
				"id": p.ID, "product_id": p.ProductID, "provider": p.Provider,
				"status": p.Status, "credits": p.Credits, "refunded_credits": p.RefundedCredits,
				"environment": p.Environment, "created_at": p.CreatedAt, "completed_at": nullTime(p.CompletedAt),
			}
			if p.Amount != nil {
				item["amount"] = *p.Amount
			}
			if p.Currency != nil {
				item["currency"] = *p.Currency
			}
			purchases = append(purchases, item)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "email": u.Email, "display_name": u.DisplayName,
		"status": u.Status, "balance": bal.Balance, "created_at": u.CreatedAt,
		"identities": []any{}, "purchases": purchases,
	})
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Status != auth.StatusActive && body.Status != auth.StatusDisabled {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid status"}))
		return
	}
	now := db.Now()
	if err := h.users.UpdateStatus(r.Context(), nil, appID, userID, body.Status, now); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			httpx.WriteError(w, r, auth.ErrUserNotFound)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	if body.Status == auth.StatusDisabled {
		_ = h.users.RevokeAllRefreshForUser(r.Context(), nil, appID, userID, now)
	}
	_ = h.writeAudit(r, &appID, "user.update", "user", userID.String(), map[string]any{"status": body.Status})
	u, _ := h.users.GetByID(r.Context(), nil, appID, userID)
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (h *Handler) userLedger(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.credits.ListLedger(r.Context(), appID, userID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, e := range items {
		out = append(out, map[string]any{
			"id": e.ID, "entry_type": e.EntryType, "amount": e.Amount,
			"balance_after": e.BalanceAfter, "reference_type": e.ReferenceType,
			"reference_id": e.ReferenceID, "actor": e.Actor, "reason": e.Reason,
			"idempotency_key": e.IdempotencyKey, "metadata": json.RawMessage(e.Metadata),
			"created_at": e.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nil})
}

func (h *Handler) adjustCredits(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		EntryType string `json:"entry_type"`
		Amount    int64  `json:"amount"`
		Reason    string `json:"reason"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.EntryType != credit.TypeAdminAdjust && body.EntryType != credit.TypeAffiliatePayout {
		httpx.WriteError(w, r, credit.ErrInvalidEntryType)
		return
	}
	if body.EntryType == credit.TypeAffiliatePayout && body.Amount <= 0 {
		httpx.WriteError(w, r, credit.ErrInvalidAmount)
		return
	}
	if len(body.Reason) < 1 || len(body.Reason) > 500 {
		httpx.WriteError(w, r, credit.ErrReasonRequired)
		return
	}
	if _, err := h.users.GetByID(r.Context(), nil, appID, userID); err != nil {
		httpx.WriteError(w, r, auth.ErrUserNotFound)
		return
	}
	auditID := uuid.Must(uuid.NewV7())
	res, err := h.credits.Apply(r.Context(), credit.ApplyInput{
		AppID: appID, UserID: userID, EntryType: body.EntryType, Amount: body.Amount,
		IdempotencyKey: "admin:" + auditID.String(), ReferenceType: "admin", ReferenceID: auditID.String(),
		Actor: "admin", Reason: body.Reason,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.audit.Insert(r.Context(), nil, AuditEntry{
		ID: auditID, Actor: "admin", Action: "credits.adjust", AppID: &appID,
		TargetType: "user", TargetID: userID.String(),
		Diff: MustJSON(RedactDiffMap(map[string]any{
			"entry_type": body.EntryType, "amount": body.Amount, "reason": body.Reason,
		})),
		IP: httpx.ClientIP(r, nil), RequestID: r.Header.Get("X-Request-ID"), CreatedAt: db.Now(),
	})
	httpx.WriteJSON(w, http.StatusCreated, res.Entry)
}

func (h *Handler) clearDebt(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if _, err := h.users.GetByID(r.Context(), nil, appID, userID); err != nil {
		httpx.WriteError(w, r, auth.ErrUserNotFound)
		return
	}
	auditID := uuid.Must(uuid.NewV7())
	res, err := h.credits.ClearDebt(r.Context(), appID, userID, body.Reason, auditID.String())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.audit.Insert(r.Context(), nil, AuditEntry{
		ID: auditID, Actor: "admin", Action: "credits.clear_debt", AppID: &appID,
		TargetType: "user", TargetID: userID.String(),
		Diff: MustJSON(map[string]any{"cleared": res.Cleared, "reason": body.Reason}),
		IP:   httpx.ClientIP(r, nil), RequestID: r.Header.Get("X-Request-ID"), CreatedAt: db.Now(),
	})
	status := http.StatusOK
	if res.Cleared > 0 {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, res)
}

func parseAppUser(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	appID, err := parseAppID(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	userID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		return uuid.Nil, uuid.Nil, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid user_id"})
	}
	return appID, userID, nil
}
