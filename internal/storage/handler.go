package storage

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// Handler serves /files endpoints.
type Handler struct{ svc *Service }

// NewHandler constructs a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Create handles POST /files.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	var body struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Create(r.Context(), app, userID, CreateInput{
		Filename: body.Filename, ContentType: body.ContentType, SizeBytes: body.SizeBytes,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"file": res.File.Public(),
		"upload": map[string]any{
			"method": res.Upload.Method, "url": res.Upload.URL,
			"headers": res.Upload.Headers, "expires_at": res.Upload.ExpiresAt,
		},
	})
}

// Complete handles POST /files/{file_id}/complete.
func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "file_id"))
	if err != nil {
		httpx.WriteError(w, r, ErrFileNotFound)
		return
	}
	f, err := h.svc.Complete(r.Context(), app, userID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"file": f.Public()})
}

// List handles GET /files.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, next, err := h.svc.List(r.Context(), app.ID, userID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, f := range items {
		out = append(out, f.Public())
	}
	resp := map[string]any{"items": out}
	if next != "" {
		resp["next_cursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Get handles GET /files/{file_id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "file_id"))
	if err != nil {
		httpx.WriteError(w, r, ErrFileNotFound)
		return
	}
	f, url, exp, err := h.svc.Get(r.Context(), app, userID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"file":     f.Public(),
		"download": map[string]any{"url": url, "expires_at": exp},
	})
}

// Delete handles DELETE /files/{file_id}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	app, userID, ok := userApp(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "file_id"))
	if err != nil {
		httpx.WriteError(w, r, ErrFileNotFound)
		return
	}
	if err := h.svc.Delete(r.Context(), app, userID, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
