package email_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

func TestRenderStoredEscapesHTML(t *testing.T) {
	subject, html, text, err := email.RenderStored(
		"Hi {{{NAME}}}",
		"<p>{{{NAME}}}</p>",
		"{{{NAME}}}",
		map[string]string{"NAME": "<b>"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Hi <b>" || text != "<b>" {
		t.Fatalf("plain subject=%q text=%q", subject, text)
	}
	if !strings.Contains(html, "&lt;b&gt;") || strings.Contains(html, "<b>") {
		t.Fatalf("html %q", html)
	}
}

func TestTemplateOverrideAndCustomSend(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: []byte("0123456789abcdef0123456789abcdef")}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DefaultConnections: config.DefaultConnections{Resend: &config.ResendDefault{APIKey: "re"}}}
	env := integration.BuildEnvDefaults(cfg)
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		Email: tenant.EmailSettings{FromName: "Acme", FromAddress: "n@acme.com", ReplyTo: "help@acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := email.NewStore(database)
	ob := outbox.NewStore(database)
	boxSecrets := box
	pub := outbox.NewPublisher(ob, boxSecrets, registryWithEmail())
	svc := email.NewService(database, store, pub)

	fallback := json.RawMessage(`"item"`)
	tpl, err := svc.Create(context.Background(), app.ID, email.Input{
		Key: "verify-brand", Name: "Verify", Subject: "Verify {{{APP_NAME}}}",
		HTML:      `<p><a href="{{{ACTION_URL}}}">{{{PRODUCT}}}</a></p>`,
		Text:      "{{{ACTION_URL}}}",
		Variables: []email.Variable{{Key: "PRODUCT", Type: "string", Fallback: &fallback}},
		Published: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetTrigger(context.Background(), app.ID, email.TriggerVerifyEmail, &tpl.ID); err != nil {
		t.Fatal(err)
	}

	tpls, _ := email.LoadTemplates()
	mem := &email.MemorySender{}
	sub := email.NewSubscriber(mem, tpls, integration.NewResolver(integration.NewStore(database), box, env), tSvc)
	sub.SetCatalog(store)
	raw, _ := json.Marshal(email.Payload{
		Template: "verify_email", To: "u@ex.com", ActionURL: "https://app/verify?token=abc",
	})
	row := outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &app.ID, EventType: outbox.EventEmailRequested, Subscriber: outbox.SubEmail}
	if err := sub.Handle(context.Background(), row, raw); err != nil {
		t.Fatal(err)
	}
	if len(mem.Messages) != 1 || !strings.Contains(mem.Messages[0].HTML, "https://app/verify?token=abc") || !strings.Contains(mem.Messages[0].HTML, "item") {
		t.Fatalf("override %#v", mem.Messages)
	}
	if mem.Messages[0].Subject != "Verify Acme" {
		t.Fatalf("subject %q", mem.Messages[0].Subject)
	}

	custom, err := svc.Create(context.Background(), app.ID, email.Input{
		Key: "order-confirmation", Name: "Order", Subject: "Thanks",
		HTML: "<p>{{{PRODUCT}}}</p>", Text: "{{{PRODUCT}}}",
		Variables: []email.Variable{{Key: "PRODUCT", Type: "string"}},
		Published: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.RequestSend(context.Background(), app, "u@ex.com", "idem-1", custom.Key, map[string]any{"PRODUCT": "Pro"})
	if err != nil || id == uuid.Nil {
		t.Fatalf("send id=%s err=%v", id, err)
	}
	again, err := svc.RequestSend(context.Background(), app, "u@ex.com", "idem-1", custom.Key, map[string]any{"PRODUCT": "Pro"})
	if err != nil || again != id {
		t.Fatalf("replay id=%s want %s err=%v", again, id, err)
	}
	if _, err := svc.RequestSend(context.Background(), app, "u@ex.com", "idem-1", custom.Key, map[string]any{"PRODUCT": "Other"}); err == nil {
		t.Fatal("expected idempotency conflict")
	}
	if _, err := svc.RequestSend(context.Background(), app, "u@ex.com", "idem-2", tpl.Key, nil); err == nil {
		t.Fatal("security-bound template must not be client-sendable")
	}
}

func registryWithEmail() *outbox.Registry {
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	return reg
}
