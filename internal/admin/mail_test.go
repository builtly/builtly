package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

type captureSender struct {
	conn integration.Resolved
	msg  email.Message
	id   string
	err  error
}

func (c *captureSender) Send(_ context.Context, conn integration.Resolved, msg email.Message) (string, error) {
	c.conn = conn
	c.msg = msg
	if c.err != nil {
		return "", c.err
	}
	if c.id == "" {
		c.id = "re_test_id"
	}
	return c.id, nil
}

func TestConnectionTestEmail(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Env:       "development",
		PublicURL: "https://api.example.com",
		DefaultConnections: config.DefaultConnections{
			Resend: &config.ResendDefault{APIKey: "re_env_default_secret"},
		},
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	audit := admin.NewAuditStore(database)
	h := admin.NewHandler(database, tSvc, iSvc, resolver, audit, tStore, iStore, tenant.NewMiddleware(tSvc, 600),
		auth.NewStore(database), credit.NewService(database, credit.NewStore(database)),
		billing.NewStore(database), outbox.NewStore(database), box, nil, nil, nil)
	sender := &captureSender{id: "re_sent"}
	h.SetMailSender(sender)
	r := chi.NewRouter()
	h.Routes(r)

	pc, err := iSvc.CreateConnection(context.Background(), integration.ProviderResend, "Mail",
		json.RawMessage(`{}`), json.RawMessage(`{"api_key":"re_conn_secret"}`))
	if err != nil {
		t.Fatal(err)
	}

	rr := postTestEmail(t, r, pc.ID, map[string]any{
		"provider": "resend",
		"title":    "Hello",
		"from":     "Ops <from@example.com>",
		"to":       "to@example.com",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rr.Code, rr.Body.String())
	}
	if sender.msg.Subject != "Hello" || sender.msg.From != "from@example.com" || sender.msg.To != "to@example.com" {
		t.Fatalf("message %+v", sender.msg)
	}
	if !strings.Contains(sender.msg.HTML, "Hello") || strings.Contains(sender.msg.Text, "<") {
		t.Fatalf("body html=%q text=%q", sender.msg.HTML, sender.msg.Text)
	}
	var sec integration.APIKeySecrets
	if err := json.Unmarshal(sender.conn.Secrets, &sec); err != nil || sec.APIKey != "re_conn_secret" {
		t.Fatalf("opened secrets %#v err=%v", sec, err)
	}
	if strings.Contains(rr.Body.String(), "re_conn_secret") {
		t.Fatal("response leaked api key")
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.ID != "re_sent" {
		t.Fatalf("response %s", rr.Body.String())
	}
	rows, err := audit.List(context.Background(), nil, "connection.test_email", 10, db.Time{}, uuid.Nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows=%d err=%v", len(rows), err)
	}
	if strings.Contains(string(rows[0].Diff), "re_conn_secret") {
		t.Fatalf("audit leaked secret: %s", rows[0].Diff)
	}

	rr = postTestEmail(t, r, "default", map[string]any{
		"provider": "resend", "title": "Env", "from": "a@example.com", "to": "b@example.com",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("env default: %d %s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(sender.conn.Secrets, &sec); err != nil || sec.APIKey != "re_env_default_secret" {
		t.Fatalf("env secrets %#v", sec)
	}

	rr = postTestEmail(t, r, pc.ID, map[string]any{
		"provider": "stripe", "title": "Nope", "from": "a@example.com", "to": "b@example.com",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong provider: %d %s", rr.Code, rr.Body.String())
	}

	rr = postTestEmail(t, r, pc.ID, map[string]any{
		"provider": "resend", "title": "Nope", "from": "not-an-email", "to": "b@example.com",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad sender: %d %s", rr.Code, rr.Body.String())
	}

	sender.err = outbox.ErrPermanent{Err: io.EOF}
	rr = postTestEmail(t, r, pc.ID, map[string]any{
		"provider": "resend", "title": "Nope", "from": "a@example.com", "to": "b@example.com",
	})
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "email_send_failed") {
		t.Fatalf("send failure: %d %s", rr.Code, rr.Body.String())
	}
}

func postTestEmail(t *testing.T, h http.Handler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/connections/"+id+"/test-email", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}
