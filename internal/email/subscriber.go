package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// Payload is the sealed JSON for email.requested / user.registered email fan-out.
type Payload struct {
	UserID         string         `json:"user_id,omitempty"`
	Template       string         `json:"template,omitempty"` // built-in name: verify_email | reset_password | email_changed
	TemplateKey    string         `json:"template_key,omitempty"`
	Trigger        string         `json:"trigger,omitempty"`
	Kind           string         `json:"kind,omitempty"` // system | custom
	To             string         `json:"to"`
	ActionURL      string         `json:"action_url,omitempty"`
	Expiry         string         `json:"expiry,omitempty"`
	PasswordSignup bool           `json:"password_signup,omitempty"` // for user.registered
	Variables      map[string]any `json:"variables,omitempty"`
}

// Subscriber delivers email outbox rows via Resend (or a test Sender).
type Subscriber struct {
	sender    Sender
	templates *Templates
	resolver  *integration.Resolver
	apps      *tenant.Service
	catalog   *Store
}

// SetCatalog enables per-app template overrides and custom sends.
func (s *Subscriber) SetCatalog(store *Store) { s.catalog = store }

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
	if p.Template == "" && p.Kind != KindCustom && p.Trigger == "" {
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
	subject, html, text, err := s.render(ctx, app, p)
	if err != nil {
		return err
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

func (s *Subscriber) render(ctx context.Context, app tenant.App, p Payload) (string, string, string, error) {
	if p.Kind == KindCustom {
		return s.renderCustom(ctx, app, p)
	}
	trigger := p.Trigger
	if trigger == "" {
		trigger = TriggerForBuiltin(p.Template)
	}
	if s.catalog != nil && trigger != "" {
		b, err := s.catalog.GetBinding(ctx, nil, app.ID, trigger)
		if err == nil {
			t, gerr := s.catalog.Get(ctx, nil, app.ID, b.TemplateID)
			if gerr != nil {
				return "", "", "", outbox.ErrRetryable{Err: gerr}
			}
			if !t.Published {
				return "", "", "", outbox.ErrPermanent{Err: fmt.Errorf("template unpublished")}
			}
			vars, rerr := resolveVars(t, nil, systemVars(app, p))
			if rerr != nil {
				return "", "", "", outbox.ErrPermanent{Err: rerr}
			}
			subject, html, text, rerr := RenderStored(t.Subject, t.HTML, t.Text, vars)
			if rerr != nil {
				return "", "", "", outbox.ErrPermanent{Err: rerr}
			}
			return subject, html, text, nil
		}
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return "", "", "", outbox.ErrRetryable{Err: err}
		}
	}
	if trigger == TriggerWelcome {
		return "", "", "", outbox.ErrNotConfigured
	}
	name := p.Template
	if name == "" {
		name = BuiltinForTrigger(trigger)
	}
	if name == "" {
		return "", "", "", outbox.ErrPermanent{Err: fmt.Errorf("email: unknown template")}
	}
	subject, html, text, err := s.templates.Render(name, TemplateData{
		AppName: app.Name, ActionURL: p.ActionURL, Expiry: p.Expiry, SupportAddress: app.Settings.Email.ReplyTo,
	})
	if err != nil {
		return "", "", "", outbox.ErrPermanent{Err: err}
	}
	return subject, html, text, nil
}

func (s *Subscriber) renderCustom(ctx context.Context, app tenant.App, p Payload) (string, string, string, error) {
	if s.catalog == nil || p.TemplateKey == "" {
		return "", "", "", outbox.ErrPermanent{Err: fmt.Errorf("email: custom template unavailable")}
	}
	t, err := s.catalog.GetByKey(ctx, nil, app.ID, p.TemplateKey)
	if errors.Is(err, db.ErrNotFound) {
		return "", "", "", outbox.ErrPermanent{Err: fmt.Errorf("email: template %s not found", p.TemplateKey)}
	}
	if err != nil {
		return "", "", "", outbox.ErrRetryable{Err: err}
	}
	if !t.Published {
		return "", "", "", outbox.ErrPermanent{Err: fmt.Errorf("template unpublished")}
	}
	vars, err := resolveVars(t, p.Variables, map[string]string{
		VarAppName: app.Name, VarEmail: p.To, VarSupportAddress: app.Settings.Email.ReplyTo,
	})
	if err != nil {
		return "", "", "", outbox.ErrPermanent{Err: err}
	}
	subject, html, text, err := RenderStored(t.Subject, t.HTML, t.Text, vars)
	if err != nil {
		return "", "", "", outbox.ErrPermanent{Err: err}
	}
	return subject, html, text, nil
}

func systemVars(app tenant.App, p Payload) map[string]string {
	return map[string]string{
		VarAppName: app.Name, VarEmail: p.To, VarSupportAddress: app.Settings.Email.ReplyTo,
		VarActionURL: p.ActionURL, VarExpiry: p.Expiry,
	}
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
