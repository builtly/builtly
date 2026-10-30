package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/platform/httpx"
)

func (h *Handler) requireEmails(w http.ResponseWriter, r *http.Request) bool {
	if h.emails != nil {
		return true
	}
	httpx.WriteError(w, r, apperr.Internal)
	return false
}

func (h *Handler) listEmailTemplates(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.requireEmails(w, r) {
		return
	}
	items, err := h.emails.List(r.Context(), appID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getEmailTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, id, err := parseTemplateID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.emails.Get(r.Context(), appID, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

func (h *Handler) createEmailTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := decodeTemplateInput(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.emails.Create(r.Context(), appID, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "email_template.create", "email_template", t.ID.String(), map[string]any{
		"key": t.Key, "published": t.Published,
	})
	httpx.WriteJSON(w, http.StatusCreated, t)
}

func (h *Handler) patchEmailTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, id, err := parseTemplateID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := decodeTemplateInput(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.emails.Update(r.Context(), appID, id, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "email_template.update", "email_template", t.ID.String(), map[string]any{
		"key": t.Key, "published": t.Published,
	})
	httpx.WriteJSON(w, http.StatusOK, t)
}

func (h *Handler) deleteEmailTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, id, err := parseTemplateID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.emails.Delete(r.Context(), appID, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "email_template.delete", "email_template", id.String(), map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listEmailTriggers(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.emails.ListTriggers(r.Context(), appID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) putEmailTrigger(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmails(w, r) {
		return
	}
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	trigger := chi.URLParam(r, "trigger")
	var body struct {
		TemplateID *string `json:"template_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var id *uuid.UUID
	if body.TemplateID != nil && *body.TemplateID != "" {
		parsed, err := uuid.Parse(*body.TemplateID)
		if err != nil {
			httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid template_id"}))
			return
		}
		id = &parsed
	}
	if err := h.emails.SetTrigger(r.Context(), appID, trigger, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "email_trigger.update", "email_trigger", trigger, map[string]any{
		"template_id": body.TemplateID,
	})
	items, err := h.emails.ListTriggers(r.Context(), appID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func parseTemplateID(r *http.Request) (uuid.UUID, uuid.UUID, error) {
	appID, err := parseAppID(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	id, err := uuid.Parse(chi.URLParam(r, "template_id"))
	if err != nil {
		return uuid.Nil, uuid.Nil, email.ErrTemplateNotFound
	}
	return appID, id, nil
}

func decodeTemplateInput(r *http.Request) (email.Input, error) {
	var body struct {
		Key       string           `json:"key"`
		Name      string           `json:"name"`
		Subject   string           `json:"subject"`
		HTML      string           `json:"html"`
		Text      string           `json:"text"`
		Variables []email.Variable `json:"variables"`
		Published bool             `json:"published"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return email.Input{}, err
	}
	if body.Variables == nil {
		body.Variables = []email.Variable{}
	}
	return email.Input{
		Key: body.Key, Name: body.Name, Subject: body.Subject, HTML: body.HTML, Text: body.Text,
		Variables: body.Variables, Published: body.Published,
	}, nil
}
