package analytics_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/analytics"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/tenant"
)

func TestAnalyticsCapture(t *testing.T) {
	var hits atomic.Int32
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &last)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	database := dbtest.New(t, "sqlite")
	key := make([]byte, 32)
	for i := range key {
		key[i] = 1
	}
	box, _ := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	cfg := &config.Config{PublicURL: "https://api.example.com"}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)

	app, err := tSvc.Create(context.Background(), "acme", "Acme", tenant.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := iSvc.CreateConnection(context.Background(), integration.ProviderPostHog, "ph",
		json.RawMessage(`{"host":"https://ph.example"}`),
		json.RawMessage(`{"project_api_key":"phc_test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := iSvc.ReplaceBindingsForApp(context.Background(), app.ID, map[string]integration.BindingInput{
		integration.ProviderPostHog: {ConnectionID: conn.ID},
	}); err != nil {
		t.Fatal(err)
	}

	client := analytics.NewClient(srv.Client())
	client.BaseURL = srv.URL
	sub := analytics.NewSubscriber(client, resolver)

	uid := uuid.Must(uuid.NewV7())
	payload, _ := json.Marshal(map[string]any{"user_id": uid.String()})
	row := outbox.Row{
		ID: uuid.Must(uuid.NewV7()), AppID: &app.ID,
		EventType: outbox.EventUserRegistered, Subscriber: outbox.SubAnalytics,
	}
	if err := sub.Handle(context.Background(), row, payload); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
	if last["event"] != "user_signed_up" || last["uuid"] != row.ID.String() {
		t.Fatalf("body %+v", last)
	}
	if last["distinct_id"] != uid.String() {
		t.Fatalf("distinct %v", last["distinct_id"])
	}
}
