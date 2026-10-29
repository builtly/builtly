package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/referral"
)

// listReferrals handles GET /apps/{app_id}/referrals.
func (h *Handler) listReferrals(w http.ResponseWriter, r *http.Request) {
	if h.referrals == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var referrerID *uuid.UUID
	if v := r.URL.Query().Get("referrer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.WriteError(w, r, referral.ErrValidation)
			return
		}
		referrerID = &id
	}
	status := r.URL.Query().Get("status")
	limit := clampPageLimit(r.URL.Query().Get("limit"))
	rows, next, err := h.referrals.ListAdmin(r.Context(), appID, referrerID, status, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	total, err := h.referrals.CountAdmin(r.Context(), appID, referrerID, status)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"id": row.ID.String(), "code": row.Code, "status": row.Status,
			"reward_count": row.RewardCount, "rewarded_credits": row.RewardedCredits,
			"created_at": row.CreatedAt,
			"referrer":   map[string]any{"id": row.ReferrerUserID.String()},
			"referee":    map[string]any{"id": row.RefereeUserID.String()},
		}
		if u, err := h.users.GetByID(r.Context(), nil, appID, row.ReferrerUserID); err == nil {
			item["referrer"] = map[string]any{"id": u.ID.String(), "email": u.Email}
		}
		if u, err := h.users.GetByID(r.Context(), nil, appID, row.RefereeUserID); err == nil {
			item["referee"] = map[string]any{"id": u.ID.String(), "email": u.Email}
		}
		items = append(items, item)
	}
	resp := map[string]any{"items": items, "total": total, "next_cursor": nil}
	if next != "" {
		resp["next_cursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// voidReferral handles POST /apps/{app_id}/referrals/{referral_id}/void.
func (h *Handler) voidReferral(w http.ResponseWriter, r *http.Request) {
	if h.referrals == nil {
		httpx.WriteError(w, r, referral.ErrNotFound)
		return
	}
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	refID, err := uuid.Parse(chi.URLParam(r, "referral_id"))
	if err != nil {
		httpx.WriteError(w, r, referral.ErrNotFound)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = httpx.DecodeJSON(r, &body)
	row, err := h.referrals.VoidAdmin(r.Context(), appID, refID, body.Reason)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "referral.void", "referral", refID.String(), map[string]any{
		"reason": body.Reason,
	})
	httpx.WriteJSON(w, http.StatusOK, row)
}
