package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	after, afterID, err := parseUsersCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid cursor"}))
		return
	}
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	users, err := h.users.List(r.Context(), nil, appID, q, status, limit, after, afterID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	total, err := h.users.Count(r.Context(), nil, appID, q, status)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		bal, _ := h.credits.GetBalance(r.Context(), appID, u.ID)
		items = append(items, map[string]any{
			"id": u.ID, "email": u.Email, "status": u.Status,
			"display_name": u.DisplayName, "avatar_url": u.AvatarURL,
			"locale": u.Locale, "last_ip": u.LastIP, "utm_source": attributionUTM(u.Attribution),
			"balance": bal.Balance, "created_at": u.CreatedAt, "last_login_at": nullTime(u.LastLoginAt),
		})
	}
	resp := map[string]any{"items": items, "total": total, "next_cursor": nil}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if len(users) == limit && limit > 0 {
		last := users[len(users)-1]
		resp["next_cursor"] = time.Time(last.CreatedAt).UTC().Format(time.RFC3339Nano) + "," + last.ID.String()
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func parseUsersCursor(cursor string) (db.Time, uuid.UUID, error) {
	t, id, err := httpx.DecodeCursor(cursor)
	if err != nil {
		return db.Time{}, uuid.Nil, err
	}
	return db.Time(t), id, nil
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
	identities := []any{}
	if idents, err := h.users.ListIdentities(r.Context(), nil, appID, userID); err == nil {
		identities = make([]any, 0, len(idents))
		for _, id := range idents {
			identities = append(identities, map[string]any{
				"provider": id.Provider, "email": id.Email,
			})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "email": u.Email, "display_name": u.DisplayName,
		"avatar_url": u.AvatarURL, "locale": u.Locale, "last_ip": u.LastIP,
		"utm_source": attributionUTM(u.Attribution),
		"status":     u.Status, "balance": bal.Balance, "created_at": u.CreatedAt,
		"last_login_at": nullTime(u.LastLoginAt),
		"identities":    identities, "purchases": purchases,
	})
}

func attributionUTM(attr []byte) string {
	if len(attr) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(attr, &m); err != nil {
		return ""
	}
	v, _ := m["utm_source"].(string)
	return strings.TrimSpace(v)
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Status   string `json:"status"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Status == "" && body.Password == "" {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "status or password required"}))
		return
	}
	if body.Status != "" && body.Status != auth.StatusActive && body.Status != auth.StatusDisabled {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid status"}))
		return
	}
	if body.Password != "" {
		if err := auth.ValidatePasswordLength(body.Password); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	curUser, err := h.users.GetByID(r.Context(), nil, appID, userID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			httpx.WriteError(w, r, auth.ErrUserNotFound)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	if curUser.Status == auth.StatusDeleted {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "deleted user cannot be modified"}))
		return
	}
	now := db.Now()
	audit := map[string]any{}
	if body.Status != "" {
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
		audit["status"] = body.Status
	}
	if body.Password != "" {
		hash, err := auth.HashPassword(body.Password)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if err := h.users.UpdatePasswordHash(r.Context(), nil, appID, userID, hash, now); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		_ = h.users.RevokeAllRefreshForUser(r.Context(), nil, appID, userID, now)
		audit["password"] = "reset"
	}
	_ = h.writeAudit(r, &appID, "user.update", "user", userID.String(), audit)
	u, _ := h.users.GetByID(r.Context(), nil, appID, userID)
	httpx.WriteJSON(w, http.StatusOK, u)
}

func (h *Handler) userLedger(w http.ResponseWriter, r *http.Request) {
	appID, userID, err := parseAppUser(r)
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
	items, err := h.credits.ListLedgerPaged(r.Context(), appID, userID, limit, after, afterID)
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
	var nextCursor any
	if len(items) == limit && limit > 0 {
		last := items[len(items)-1]
		nextCursor = keysetCursor(last.CreatedAt, last.ID)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "next_cursor": nextCursor})
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
		IP: httpx.ClientIP(r, h.trustedProxies), RequestID: r.Header.Get("X-Request-ID"), CreatedAt: db.Now(),
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
		IP:   httpx.ClientIP(r, h.trustedProxies), RequestID: r.Header.Get("X-Request-ID"), CreatedAt: db.Now(),
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
