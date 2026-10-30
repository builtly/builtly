package admin

import (
	"errors"
	"html"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/httpx"
)

const maxTestEmailTitle = 200

// SetMailSender overrides the client used by connection test sends.
// Nil restores the default Resend client.
func (h *Handler) SetMailSender(s email.Sender) {
	h.mail = s
}

func (h *Handler) mailSender() email.Sender {
	if h.mail != nil {
		return h.mail
	}
	return email.NewResendClient(nil)
}

func (h *Handler) testConnectionEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Title    string `json:"title"`
		From     string `json:"from"`
		To       string `json:"to"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Provider != integration.ProviderResend {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{
			"reason": "test email is only available for resend",
		}))
		return
	}
	title, from, to, err := parseTestEmail(body.Title, body.From, body.To)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id := chi.URLParam(r, "connection_id")
	conn, err := h.integ.OpenConnection(r.Context(), id, body.Provider)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if conn.Provider != integration.ProviderResend {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{
			"reason": "test email is only available for resend",
		}))
		return
	}
	text := "This is a test email from Builtly.\n\n" + title
	msg := email.Message{
		From:    from,
		To:      to,
		Subject: title,
		Text:    text,
		HTML:    "<p>This is a test email from Builtly.</p><p>" + html.EscapeString(title) + "</p>",
	}
	providerID, err := h.mailSender().Send(r.Context(), conn, msg)
	if err != nil {
		httpx.WriteError(w, r, testEmailSendError(err))
		return
	}
	_ = h.writeAudit(r, nil, "connection.test_email", "connection", id, map[string]any{
		"provider": body.Provider, "from": from, "to": to, "title": title, "provider_id": providerID,
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": providerID})
}

func parseTestEmail(title, from, to string) (string, string, string, error) {
	title = strings.TrimSpace(title)
	if title == "" || strings.ContainsAny(title, "\r\n") || len(title) > maxTestEmailTitle {
		return "", "", "", apperr.BadRequest.WithDetails(map[string]any{"reason": "title is required"})
	}
	from, err := parseMailbox(from, "sender")
	if err != nil {
		return "", "", "", err
	}
	to, err = parseMailbox(to, "receiver")
	if err != nil {
		return "", "", "", err
	}
	return title, from, to, nil
}

func parseMailbox(raw, field string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n") || len(raw) > 320 {
		return "", apperr.BadRequest.WithDetails(map[string]any{"reason": field + " must be an email address"})
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address == "" || !strings.Contains(addr.Address, "@") {
		return "", apperr.BadRequest.WithDetails(map[string]any{"reason": field + " must be an email address"})
	}
	return addr.Address, nil
}

func testEmailSendError(err error) error {
	reason := truncateReason(err.Error())
	var retry outbox.ErrRetryable
	if errors.As(err, &retry) {
		return apperr.UpstreamError.WithDetails(map[string]any{"reason": reason})
	}
	var perm outbox.ErrPermanent
	if errors.As(err, &perm) {
		return apperr.New(http.StatusUnprocessableEntity, "email_send_failed", "test email failed").WithDetails(map[string]any{
			"reason": reason,
		})
	}
	return apperr.UpstreamError.WithDetails(map[string]any{"reason": reason})
}

func truncateReason(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 300 {
		return s
	}
	return s[:300]
}
