package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
)

func (h *Handler) listAppPurchases(w http.ResponseWriter, r *http.Request) {
	if h.purchases == nil {
		httpx.WriteError(w, r, apperr.Internal)
		return
	}
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	provider := r.URL.Query().Get("provider")
	if !validPurchaseStatus(status) || !validPurchaseProvider(provider) {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid filter"}))
		return
	}
	userID, userQ, err := activityUserFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := clampPageLimit(r.URL.Query().Get("limit"))
	after, afterID, err := parseUsersCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid cursor"}))
		return
	}
	filter := billing.PurchaseListQuery{
		Status: status, Provider: provider, UserID: userID, UserQ: userQ,
		Limit: limit, After: after, AfterID: afterID,
	}
	rows, err := h.purchases.ListAppPurchases(r.Context(), nil, appID, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	total, err := h.purchases.CountAppPurchases(r.Context(), nil, appID, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	emails := map[uuid.UUID]string{}
	skus := map[uuid.UUID]string{}
	items := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		item := map[string]any{
			"id": p.ID, "user_id": p.UserID, "user_email": h.userEmail(r.Context(), appID, p.UserID, emails),
			"product_id": p.ProductID, "provider": p.Provider, "status": p.Status,
			"credits": p.Credits, "refunded_credits": p.RefundedCredits,
			"environment": p.Environment, "created_at": p.CreatedAt, "completed_at": nullTime(p.CompletedAt),
		}
		if sku := h.productSKU(r.Context(), appID, p.ProductID, skus); sku != "" {
			item["product_sku"] = sku
		}
		if p.Amount != nil {
			item["amount"] = *p.Amount
		}
		if p.Currency != nil {
			item["currency"] = *p.Currency
		}
		if p.ProviderRef != nil {
			item["provider_ref"] = *p.ProviderRef
		}
		if p.PaymentRef != nil {
			item["payment_ref"] = *p.PaymentRef
		}
		items = append(items, item)
	}
	resp := map[string]any{"items": items, "total": total, "next_cursor": nil}
	if len(rows) == limit && limit > 0 {
		last := rows[len(rows)-1]
		resp["next_cursor"] = keysetCursor(last.CreatedAt, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) listAppLedger(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	entryType := r.URL.Query().Get("entry_type")
	if entryType != "" && !knownEntryType(entryType) {
		httpx.WriteError(w, r, credit.ErrInvalidEntryType)
		return
	}
	userID, userQ, err := activityUserFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := clampPageLimit(r.URL.Query().Get("limit"))
	after, afterID, err := parseUsersCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid cursor"}))
		return
	}
	filter := credit.AppLedgerQuery{
		EntryType: entryType, UserID: userID, UserQ: userQ,
		Limit: limit, After: after, AfterID: afterID,
	}
	rows, err := h.credits.ListAppLedger(r.Context(), appID, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	total, err := h.credits.CountAppLedger(r.Context(), appID, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	emails := map[uuid.UUID]string{}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, map[string]any{
			"id": e.ID, "user_id": e.UserID, "user_email": h.userEmail(r.Context(), appID, e.UserID, emails),
			"entry_type": e.EntryType, "amount": e.Amount, "balance_after": e.BalanceAfter,
			"reference_type": e.ReferenceType, "reference_id": e.ReferenceID,
			"actor": e.Actor, "reason": e.Reason, "idempotency_key": e.IdempotencyKey,
			"metadata": json.RawMessage(e.Metadata), "created_at": e.CreatedAt,
		})
	}
	resp := map[string]any{"items": items, "total": total, "next_cursor": nil}
	if len(rows) == limit && limit > 0 {
		last := rows[len(rows)-1]
		resp["next_cursor"] = keysetCursor(last.CreatedAt, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func activityUserFilter(r *http.Request) (*uuid.UUID, string, error) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		return nil, "", apperr.BadRequest.WithDetails(map[string]any{"reason": "q too long"})
	}
	raw := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if raw == "" {
		return nil, q, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, "", apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid user_id"})
	}
	return &id, q, nil
}

func (h *Handler) userEmail(ctx context.Context, appID, userID uuid.UUID, cache map[uuid.UUID]string) string {
	if email, ok := cache[userID]; ok {
		return email
	}
	email := ""
	if u, err := h.users.GetByID(ctx, nil, appID, userID); err == nil && u.Email != nil {
		email = *u.Email
	}
	cache[userID] = email
	return email
}

func (h *Handler) productSKU(ctx context.Context, appID, productID uuid.UUID, cache map[uuid.UUID]string) string {
	if h.catalog == nil {
		return ""
	}
	if sku, ok := cache[productID]; ok {
		return sku
	}
	sku := ""
	if p, err := h.catalog.Get(ctx, appID, productID); err == nil {
		sku = p.SKU
	}
	cache[productID] = sku
	return sku
}

func validPurchaseStatus(s string) bool {
	switch s {
	case "", billing.StatusPending, billing.StatusCompleted, billing.StatusPartialRefunded,
		billing.StatusRefunded, billing.StatusFailed, billing.StatusExpired:
		return true
	default:
		return false
	}
}

func validPurchaseProvider(s string) bool {
	switch s {
	case "", billing.ProviderStripe, billing.ProviderApple, billing.ProviderGoogle:
		return true
	default:
		return false
	}
}

func knownEntryType(s string) bool {
	switch s {
	case credit.TypeSignupBonus, credit.TypeReferralBonus, credit.TypeStripePurchase, credit.TypeIAPPurchase,
		credit.TypeAIConsume, credit.TypeRefund, credit.TypeRefundReversal, credit.TypeAffiliatePayout,
		credit.TypeAffiliateReversal, credit.TypeAdminAdjust:
		return true
	default:
		return false
	}
}

func clampPageLimit(raw string) int {
	limit, _ := strconv.Atoi(raw)
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func keysetCursor(createdAt db.Time, id uuid.UUID) string {
	return time.Time(createdAt).UTC().Format(time.RFC3339Nano) + "," + id.String()
}
