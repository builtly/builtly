package email

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/ratelimit"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// RecipientDirectory resolves the signed-in user's address. Arbitrary recipients are not accepted.
type RecipientDirectory interface {
	PrimaryEmail(ctx context.Context, appID, userID uuid.UUID) (string, error)
}

// Handler serves POST /email/send.
type Handler struct {
	svc   *Service
	users RecipientDirectory
	limit *ratelimit.Limiter
}

// NewHandler constructs the public email handler.
func NewHandler(svc *Service, users RecipientDirectory) *Handler {
	return &Handler{svc: svc, users: users, limit: ratelimit.New(10, 10)}
}

// Send enqueues one published custom template to the current user.
func (h *Handler) Send(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, tenant.ErrAppNotFound)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.UserID == uuid.Nil {
		httpx.WriteError(w, r, apperrUnauthorized())
		return
	}
	if !h.limit.Allow(app.ID.String() + ":" + p.UserID.String()) {
		httpx.WriteError(w, r, ErrRateLimited())
		return
	}
	var body struct {
		Template  string         `json:"template"`
		Variables map[string]any `json:"variables"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	to, err := h.users.PrimaryEmail(r.Context(), app.ID, p.UserID)
	if errors.Is(err, db.ErrNotFound) || to == "" {
		httpx.WriteError(w, r, ErrNoRecipient)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := h.svc.RequestSend(r.Context(), app, to, r.Header.Get("Idempotency-Key"), body.Template, body.Variables)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

func apperrUnauthorized() error {
	return errUnauthorized
}

func ErrRateLimited() error { return errRateLimited }
