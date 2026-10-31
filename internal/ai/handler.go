package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

var featureRe = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

// NewConsumeHook returns a credit.ConsumeHook that publishes credits.consumed when enabled on the app.
func NewConsumeHook(tenants *tenant.Service, pub *outbox.Publisher) credit.ConsumeHook {
	return func(ctx context.Context, tx *sqlx.Tx, entry credit.Entry, feature string) error {
		if tenants == nil || pub == nil {
			return nil
		}
		app, err := tenants.GetTx(ctx, tx, entry.AppID)
		if err != nil || !app.Settings.Analytics.CaptureConsumeEvents {
			return nil
		}
		payload, err := json.Marshal(map[string]any{
			"user_id":         entry.UserID.String(),
			"ledger_entry_id": entry.ID.String(),
			"amount":          -entry.Amount,
			"feature":         feature,
		})
		if err != nil {
			return err
		}
		appID := entry.AppID
		return pub.PublishTx(ctx, tx, outbox.Event{
			AppID: &appID, Type: outbox.EventCreditsConsumed, Payload: payload,
		})
	}
}

// Handler serves POST /ai/consume.
type Handler struct {
	credits *credit.Service
	users   *auth.Store
}

// NewHandler constructs an AI Handler.
func NewHandler(credits *credit.Service, users *auth.Store) *Handler {
	return &Handler{credits: credits, users: users}
}

// Consume handles POST /ai/consume.
func (h *Handler) Consume(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		httpx.WriteError(w, r, credit.ErrMissingIdempotency)
		return
	}

	var body struct {
		UserID   *uuid.UUID      `json:"user_id"`
		Amount   int64           `json:"amount"`
		Feature  string          `json:"feature"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !featureRe.MatchString(body.Feature) {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid feature"}))
		return
	}
	if len(body.Metadata) > 4<<10 {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "metadata too large"}))
		return
	}

	var userID uuid.UUID
	if p.IsAPIKey {
		if !p.HasScope("credits:consume") {
			httpx.WriteError(w, r, apperr.Forbidden.WithDetails(map[string]any{"reason": "missing scope credits:consume"}))
			return
		}
		if body.UserID == nil {
			httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "user_id required with API key"}))
			return
		}
		userID = *body.UserID
	} else {
		if body.UserID != nil {
			httpx.WriteError(w, r, apperr.Forbidden.WithDetails(map[string]any{"reason": "user_id forbidden with user JWT"}))
			return
		}
		userID = p.UserID
	}
	if h.users != nil {
		u, err := h.users.GetByID(r.Context(), nil, app.ID, userID)
		if err != nil || u.Status == auth.StatusDeleted {
			httpx.WriteError(w, r, auth.ErrUserNotFound)
			return
		}
		if u.Status == auth.StatusDisabled {
			httpx.WriteError(w, r, auth.ErrAccountDisabled)
			return
		}
	}

	res, err := h.credits.Consume(r.Context(), app.ID, userID, body.Amount, idem, body.Feature, p.Actor, body.Metadata)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ledger_entry_id": res.Entry.ID,
		"amount":          body.Amount,
		"balance":         res.Balance,
		"replayed":        res.Replayed,
	})
}
