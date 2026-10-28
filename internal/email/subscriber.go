package email

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/tenant"
)

// Payload is the sealed JSON for email.requested / user.registered email fan-out.
type Payload struct {
	Template       string `json:"template"` // verify_email | reset_password | email_changed
	To             string `json:"to"`
	ActionURL      string `json:"action_url"`
	Expiry         string `json:"expiry"`
	PasswordSignup bool   `json:"password_signup,omitempty"` // for user.registered
}

// Subscriber delivers email outbox rows via Resend (or a test Sender).
type Subscriber struct {
	sender    Sender
	templates *Templates
	resolver  *integration.Resolver
	apps      *tenant.Service
}

// NewSubscriber constructs an email Subscriber.
func NewSubscriber(sender Sender, templates *Templates, resolver *integration.Resolver, apps *tenant.Service) *Subscriber {
	return &Subscriber{sender: sender, templates: templates, resolver: resolver, apps: apps}
}

// Handle is an outbox.Handler.
func (s *Subscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	if row.EventType == outbox.EventUserRegistered && !p.PasswordSignup {
		return nil // social sign-ups: no verify mail
	}
	if p.Template == "" {
		p.Template = "verify_email"
	}
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: fmt.Errorf("email: missing app_id")}
	}
	app, err := s.apps.Get(ctx, *row.AppID)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	conn, err := s.resolver.For(ctx, *row.AppID, integration.ProviderResend)
	if err == integration.ErrNotConfigured {
		return outbox.ErrNotConfigured
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	subject, html, text, err := s.templates.Render(p.Template, TemplateData{
		AppName:        app.Name,
		ActionURL:      p.ActionURL,
		Expiry:         p.Expiry,
		SupportAddress: app.Settings.Email.ReplyTo,
	})
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	fromName := app.Settings.Email.FromName
	fromAddr := app.Settings.Email.FromAddress
	if fromAddr == "" {
		return outbox.ErrNotConfigured
	}
	if fromName == "" {
		fromName = app.Name
	}
	msg := Message{
		From:           fmt.Sprintf("%s <%s>", fromName, fromAddr),
		ReplyTo:        app.Settings.Email.ReplyTo,
		To:             p.To,
		Subject:        subject,
		HTML:           html,
		Text:           text,
		IdempotencyKey: row.ID.String(),
	}
	_, err = s.sender.Send(ctx, conn, msg)
	return err
}

// BuildVerifyPayload builds JSON for a verify-email outbox event.
func BuildVerifyPayload(to, actionURL string, expiry time.Time) ([]byte, error) {
	return json.Marshal(Payload{
		Template:       "verify_email",
		To:             to,
		ActionURL:      actionURL,
		Expiry:         expiry.UTC().Format(time.RFC3339),
		PasswordSignup: true,
	})
}
