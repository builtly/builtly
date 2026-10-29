package email_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

func TestTemplatesRender(t *testing.T) {
	tpl, err := email.LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	subj, html, text, err := tpl.Render("verify_email", email.TemplateData{
		AppName: "Acme", ActionURL: "https://app/verify?token=abc", Expiry: "soon", SupportAddress: "help@acme.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if subj == "" || !strings.Contains(html, "https://app/verify?token=abc") || !strings.Contains(text, "Acme") {
		t.Fatalf("render bad: %q %q %q", subj, html, text)
	}
}

func TestResendClient(t *testing.T) {
	var gotAuth, gotIdem string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_123"}`))
	}))
	defer srv.Close()

	client := email.NewResendClient(srv.Client())
	client.Base = srv.URL
	id, err := client.Send(context.Background(), integration.Resolved{
		Secrets: mustJSON(t, integration.APIKeySecrets{APIKey: "re_test"}),
	}, email.Message{
		From: "Acme <mail@acme.com>", To: "u@ex.com", Subject: "Hi", HTML: "<p>x</p>", Text: "x",
		IdempotencyKey: "idem-1",
	})
	if err != nil || id != "re_123" {
		t.Fatalf("send: id=%q err=%v", id, err)
	}
	if gotAuth != "Bearer re_test" || gotIdem != "idem-1" {
		t.Fatalf("headers auth=%q idem=%q", gotAuth, gotIdem)
	}
	if gotBody["from"] != "Acme <mail@acme.com>" {
		t.Fatalf("body %#v", gotBody)
	}

	srv429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate`))
	}))
	defer srv429.Close()
	c429 := email.NewResendClient(srv429.Client())
	c429.Base = srv429.URL
	_, err = c429.Send(context.Background(), integration.Resolved{
		Secrets: mustJSON(t, integration.APIKeySecrets{APIKey: "k"}),
	}, email.Message{From: "a <a@b.c>", To: "u@ex.com", Subject: "s"})
	var retry outbox.ErrRetryable
	if !asRetryable(err, &retry) {
		t.Fatalf("want retryable, got %T %v", err, err)
	}

	srv400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad`))
	}))
	defer srv400.Close()
	c400 := email.NewResendClient(srv400.Client())
	c400.Base = srv400.URL
	_, err = c400.Send(context.Background(), integration.Resolved{
		Secrets: mustJSON(t, integration.APIKeySecrets{APIKey: "k"}),
	}, email.Message{From: "a <a@b.c>", To: "u@ex.com", Subject: "s"})
	var perm outbox.ErrPermanent
	if !asPermanent(err, &perm) {
		t.Fatalf("want permanent, got %T %v", err, err)
	}
}

func asRetryable(err error, target *outbox.ErrRetryable) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(outbox.ErrRetryable); ok {
		*target = e
		return true
	}
	return false
}

func asPermanent(err error, target *outbox.ErrPermanent) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(outbox.ErrPermanent); ok {
		*target = e
		return true
	}
	return false
}

func TestEmailSubscriberVerifyMail(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: []byte("0123456789abcdef0123456789abcdef")}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		PublicURL: "https://api.example.com",
		DefaultConnections: config.DefaultConnections{
			Resend: &config.ResendDefault{APIKey: "re_test_key"},
		},
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)

	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		AllowedOrigins: []string{"https://acme.com"},
		Email: tenant.EmailSettings{
			FromName: "Acme", FromAddress: "noreply@acme.com", ReplyTo: "help@acme.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tpl, err := email.LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	mem := &email.MemorySender{}
	sub := email.NewSubscriber(mem, tpl, resolver, tSvc)

	store := outbox.NewStore(database)
	reg := outbox.NewRegistry()
	reg.Subscribe(outbox.EventEmailRequested, outbox.SubEmail)
	reg.SetHandler(outbox.SubEmail, sub.Handle)

	pub := outbox.NewPublisher(store, box, reg)
	payload, err := email.BuildVerifyPayload("user@example.com", "https://app/verify?token=secrettok", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), database, outbox.Event{
		AppID: &app.ID, Type: outbox.EventEmailRequested, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	d := outbox.NewDispatcher(store, box, reg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	worked, err := d.Tick(context.Background())
	if err != nil || !worked {
		t.Fatalf("tick: worked=%v err=%v", worked, err)
	}
	if len(mem.Messages) != 1 {
		t.Fatalf("want 1 mail, got %d", len(mem.Messages))
	}
	msg := mem.Messages[0]
	if msg.To != "user@example.com" || !strings.Contains(msg.HTML, "secrettok") {
		t.Fatalf("msg %#v", msg)
	}
	if !strings.Contains(msg.From, "noreply@acme.com") {
		t.Fatalf("from %q", msg.From)
	}
	done, _ := store.List(context.Background(), outbox.StatusDone, outbox.SubEmail, 10, db.Time{}, uuid.Nil)
	if len(done) != 1 {
		t.Fatalf("done rows: %#v", done)
	}
}

func TestEmailSubscriberSkipsSocialSignup(t *testing.T) {
	database := dbtest.New(t, "sqlite")
	box, _ := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: []byte("0123456789abcdef0123456789abcdef")}})
	cfg := &config.Config{DefaultConnections: config.DefaultConnections{Resend: &config.ResendDefault{APIKey: "re"}}}
	env := integration.BuildEnvDefaults(cfg)
	tSvc := tenant.NewService(database, tenant.NewStore(database))
	resolver := integration.NewResolver(integration.NewStore(database), box, env)
	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.Settings{
		Email: tenant.EmailSettings{FromAddress: "n@acme.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := email.LoadTemplates()
	mem := &email.MemorySender{}
	sub := email.NewSubscriber(mem, tpl, resolver, tSvc)
	row := outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &app.ID, EventType: outbox.EventUserRegistered, Subscriber: outbox.SubEmail}
	raw, _ := json.Marshal(email.Payload{To: "a@b.co", PasswordSignup: false})
	if err := sub.Handle(context.Background(), row, raw); err != nil {
		t.Fatal(err)
	}
	if len(mem.Messages) != 0 {
		t.Fatalf("social signup should not send mail")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
