package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
)

func (h *Handler) listOutbox(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	subscriber := r.URL.Query().Get("subscriber")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.outbox.List(r.Context(), status, subscriber, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, h.outboxItem(row))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (h *Handler) retryOutbox(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	if err := h.outbox.RetryDead(r.Context(), id, db.Now()); err != nil {
		if err == db.ErrNotFound {
			httpx.WriteError(w, r, outbox.ErrNotFound)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, nil, "outbox.retry", "outbox", id.String(), map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) outboxItem(row outbox.Row) map[string]any {
	item := map[string]any{
		"id":              row.ID,
		"app_id":          row.AppID,
		"event_type":      row.EventType,
		"subscriber":      row.Subscriber,
		"status":          row.Status,
		"attempts":        row.Attempts,
		"next_attempt_at": row.NextAttempt,
		"last_error":      row.LastError,
		"created_at":      row.CreatedAt,
		"finished_at":     nullTime(row.FinishedAt),
	}
	aad := []byte(row.ID.String() + row.Subscriber)
	plain, err := h.box.OpenEmbedded(row.PayloadEnc, aad)
	if err != nil {
		item["payload"] = map[string]any{"_error": "decrypt_failed"}
		return item
	}
	item["payload"] = redactOutboxPayload(plain)
	return item
}

func redactOutboxPayload(raw []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_raw": "[unparseable]"}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		switch {
		case strings.Contains(lk, "token"), strings.Contains(lk, "secret"),
			strings.Contains(lk, "password"), lk == "action_url":
			out[k] = "[redacted]"
		case lk == "to" || lk == "email":
			out[k] = redactEmail(asString(v))
		default:
			out[k] = v
		}
	}
	return out
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func redactEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "[redacted]"
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 1 {
		return "*" + domain
	}
	return string(local[0]) + "***" + domain
}
