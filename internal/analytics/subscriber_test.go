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

	cases := []struct {
		event   string
		payload map[string]any
		want    string
		prop    string
		value   string
	}{
		{outbox.EventUserLoggedIn, map[string]any{"user_id": uid.String(), "method": "guest"}, "user_logged_in", "method", "guest"},
		{outbox.EventGuestCreditsTransferred, map[string]any{"user_id": uid.String(), "guest_user_id": "g", "credits": 10}, "guest_credits_transferred", "guest_user_id", "g"},
		{outbox.EventCheckoutSessionCreated, map[string]any{"user_id": uid.String(), "product_sku": "pack"}, "checkout_session_created", "provider", "stripe"},
		{outbox.EventFileUploaded, map[string]any{"user_id": uid.String(), "kind": "avatar", "content_type": "image/png"}, "file_uploaded", "kind", "avatar"},
	}
	for _, tc := range cases {
		raw, _ := json.Marshal(tc.payload)
		row := outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &app.ID, EventType: tc.event, Subscriber: outbox.SubAnalytics}
		if err := sub.Handle(context.Background(), row, raw); err != nil {
			t.Fatal(err)
		}
		if last["event"] != tc.want {
			t.Fatalf("%s event %v", tc.event, last["event"])
		}
		props, _ := last["properties"].(map[string]any)
		if props[tc.prop] != tc.value {
			t.Fatalf("%s props %+v", tc.event, props)
		}
		if _, ok := props["to"]; ok {
			t.Fatalf("%s leaked email field", tc.event)
		}
	}
}

func TestAnalyticsSkipsWithoutAPIKey(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	database := dbtest.New(t, "sqlite")
	key := make([]byte, 32)
	box, _ := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	cfg := &config.Config{PublicURL: "https://api.example.com"}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	resolver := integration.NewResolver(integration.NewStore(database), box, env)
	app, err := tSvc.Create(context.Background(), "nokey", "NoKey", tenant.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	client := analytics.NewClient(srv.Client())
	client.BaseURL = srv.URL
	sub := analytics.NewSubscriber(client, resolver)
	row := outbox.Row{ID: uuid.Must(uuid.NewV7()), AppID: &app.ID, EventType: outbox.EventUserLoggedIn, Subscriber: outbox.SubAnalytics}
	if err := sub.Handle(context.Background(), row, []byte(`{"user_id":"u","method":"pwd"}`)); err != outbox.ErrNotConfigured {
		t.Fatalf("want not configured, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("posted without api key: %d", hits.Load())
	}
}
