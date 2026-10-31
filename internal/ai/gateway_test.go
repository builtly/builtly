package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/db/dbtest"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

type aiHarness struct {
	db        *db.DB
	tSvc      *tenant.Service
	iSvc      *integration.Service
	resolver  *integration.Resolver
	credits   *credit.Service
	userStore *auth.Store
	aiSvc     *ai.Service
	handler   *ai.Handler
	app       tenant.App
	user      auth.User
	llmURL    string
}

func setupAI(t *testing.T) *aiHarness {
	t.Helper()
	database := dbtest.New(t, "sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	box, err := secrets.NewBox([]config.KeyMaterial{{ID: "1", Key: key}})
	if err != nil {
		t.Fatal(err)
	}

	// Fake upstream OpenAI-compatible LLM
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		stream, _ := req["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello world\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		resp := map[string]any{
			"id": "chatcmpl-123",
			"choices": []map[string]any{
				{
					"message": map[string]string{"role": "assistant", "content": "Hello non-stream"},
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     12,
				"completion_tokens": 6,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		PublicURL: "https://api.example.com",
		DefaultConnections: config.DefaultConnections{
			OpenAI: &config.OpenAIDefault{
				BaseURL:      upstream.URL,
				APIKey:       "sk-test-openai-key",
				DefaultModel: "gpt-4o-mini",
			},
		},
	}
	env := integration.BuildEnvDefaults(cfg)
	tStore := tenant.NewStore(database)
	tSvc := tenant.NewService(database, tStore)
	iStore := integration.NewStore(database)
	resolver := integration.NewResolver(iStore, box, env)
	iSvc := integration.NewService(database, iStore, box, env, cfg.PublicURL, resolver)
	userStore := auth.NewStore(database)
	creditStore := credit.NewStore(database)
	credits := credit.NewService(database, creditStore)
	aiStore := ai.NewStore(database)
	aiSvc := ai.NewService(database, aiStore, credits, resolver, upstream.Client())
	handler := ai.NewHandler(credits, userStore)
	handler.SetService(aiSvc)

	ctx := context.Background()
	app, err := tSvc.Create(ctx, "ai-app", "AI App", tenant.Settings{
		AI: tenant.AISettings{
			Enabled:              true,
			AllowDirectChat:      true,
			MaxConcurrentPerUser: 2,
			DefaultMaxTokens:     100,
			Pricing: tenant.AIPricingSettings{
				Mode:                     "per_request",
				DefaultCreditsPerRequest: 2,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	uid := uuid.Must(uuid.NewV7())
	email := "user@example.com"
	now := db.Now()
	user := auth.User{
		ID: uid, AppID: app.ID, Email: &email, Status: auth.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := userStore.Insert(ctx, nil, user); err != nil {
		t.Fatal(err)
	}

	// Give initial 10 credits
	if _, err := credits.Apply(ctx, credit.ApplyInput{
		AppID: app.ID, UserID: uid, EntryType: credit.TypeAdminAdjust,
		Amount: 10, IdempotencyKey: "admin:init", Actor: "admin", Reason: "init",
	}); err != nil {
		t.Fatal(err)
	}

	return &aiHarness{
		db: database, tSvc: tSvc, iSvc: iSvc, resolver: resolver,
		credits: credits, userStore: userStore, aiSvc: aiSvc,
		handler: handler, app: app, user: user, llmURL: upstream.URL,
	}
}

func TestChatCompletionsNonStream(t *testing.T) {
	h := setupAI(t)
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := principal.Principal{UserID: h.user.ID, Actor: "user:" + h.user.ID.String()}
	ctx := tenant.WithApp(req.Context(), h.app)
	ctx = principal.With(ctx, p)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.handler.ChatCompletions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	bal, err := h.credits.GetBalance(context.Background(), h.app.ID, h.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Initial 10 - 2 (per_request) = 8
	if bal.Balance != 8 {
		t.Fatalf("expected balance 8, got %d", bal.Balance)
	}
	if rr.Header().Get("X-Builtly-Credits-Charged") != "2" {
		t.Fatalf("expected header 2, got %s", rr.Header().Get("X-Builtly-Credits-Charged"))
	}
}

func TestChatCompletionsStream(t *testing.T) {
	h := setupAI(t)
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi stream"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := principal.Principal{UserID: h.user.ID, Actor: "user:" + h.user.ID.String()}
	ctx := tenant.WithApp(req.Context(), h.app)
	ctx = principal.With(ctx, p)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.handler.ChatCompletions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Hello world") {
		t.Fatalf("expected stream output, got: %s", rr.Body.String())
	}

	bal, err := h.credits.GetBalance(context.Background(), h.app.ID, h.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Balance != 8 {
		t.Fatalf("expected balance 8, got %d", bal.Balance)
	}
}

func TestChatCompletionsInsufficientCredits(t *testing.T) {
	h := setupAI(t)
	// Deduct balance to 0
	_, err := h.credits.Apply(context.Background(), credit.ApplyInput{
		AppID: h.app.ID, UserID: h.user.ID, EntryType: credit.TypeAdminAdjust,
		Amount: -10, IdempotencyKey: "admin:zero", Actor: "admin", Reason: "zero",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := principal.Principal{UserID: h.user.ID, Actor: "user:" + h.user.ID.String()}
	ctx := tenant.WithApp(req.Context(), h.app)
	ctx = principal.With(ctx, p)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.handler.ChatCompletions(rr, req)
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAIPresetsCRUDAndRun(t *testing.T) {
	h := setupAI(t)
	ctx := context.Background()

	// Create preset
	preset := ai.Preset{
		ID:                 uuid.Must(uuid.NewV7()),
		AppID:              h.app.ID,
		Slug:               "translate",
		Name:               "Translate",
		Model:              "gpt-4o-mini",
		SystemPrompt:       "You are a translator. Target: {{{TARGET}}}.",
		UserPromptTemplate: "Text: {{{TEXT}}}",
		Temperature:        0.3,
		MaxTokens:          100,
		ResponseFormat:     "text",
		CreditsCost:        2,
		Published:          true,
		CreatedAt:          db.Now(),
		UpdatedAt:          db.Now(),
	}
	if err := h.aiSvc.Store().InsertPreset(ctx, nil, preset); err != nil {
		t.Fatal(err)
	}

	// Run preset
	body := `{"variables":{"TARGET":"French","TEXT":"Hello"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/presets/translate/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := principal.Principal{UserID: h.user.ID, Actor: "user:" + h.user.ID.String()}
	rctx := tenant.WithApp(req.Context(), h.app)
	rctx = principal.With(rctx, p)

	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("slug", "translate")
	rctx = context.WithValue(rctx, chi.RouteCtxKey, chiCtx)
	req = req.WithContext(rctx)

	rr := httptest.NewRecorder()
	h.handler.RunPreset(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
