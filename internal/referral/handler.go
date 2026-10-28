package referral

import (
	"net/http"
	"strconv"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/ratelimit"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
	"github.com/google/uuid"
)

// Handler serves /user/referral*.
type Handler struct {
	svc     *Service
	applyRL *ratelimit.Limiter
}

// NewHandler constructs a Handler (apply rate limit: 10/h per user).
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc, applyRL: ratelimit.New(10, 10)}
}

// Get handles GET /user/referral.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	out, err := h.svc.GetSummary(r.Context(), app, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ListReferees handles GET /user/referral/referees.
func (h *Handler) ListReferees(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, next, err := h.svc.ListReferees(r.Context(), app.ID, userID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{"items": items}
	if next != "" {
		resp["next_cursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Apply handles POST /user/referral/apply.
func (h *Handler) Apply(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	if !h.applyRL.Allow(app.ID.String() + ":" + userID.String()) {
		httpx.WriteError(w, r, apperr.TooManyRequests)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Apply(r.Context(), app, userID, body.Code)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func userApp(w http.ResponseWriter, r *http.Request) (tenant.App, uuid.UUID, bool) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return tenant.App{}, uuid.Nil, false
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.IsAPIKey || p.UserID == uuid.Nil {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return tenant.App{}, uuid.Nil, false
	}
	return app, p.UserID, true
}
