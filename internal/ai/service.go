package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// LLMClient makes requests to OpenAI-compatible endpoints.
type LLMClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Service manages AI presets and proxies chat completions.
type Service struct {
	db       *db.DB
	store    *Store
	credits  *credit.Service
	resolver *integration.Resolver
	http     *http.Client

	mu          sync.Mutex
	concurrency map[string]int // key: appID:userID -> count
}

// NewService constructs an AI Service.
func NewService(database *db.DB, store *Store, credits *credit.Service, resolver *integration.Resolver, hc *http.Client) *Service {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Service{
		db:          database,
		store:       store,
		credits:     credits,
		resolver:    resolver,
		http:        hc,
		concurrency: make(map[string]int),
	}
}

// Store returns the underlying preset store.
func (s *Service) Store() *Store { return s.store }

// AcquireConcurrency acquires a concurrency slot for the user, returning a release func.
func (s *Service) AcquireConcurrency(appID, userID uuid.UUID, limit int) (func(), error) {
	if limit <= 0 {
		limit = 2
	}
	key := appID.String() + ":" + userID.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.concurrency[key]
	if cur >= limit {
		return nil, ErrConcurrentLimit
	}
	s.concurrency[key] = cur + 1
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.concurrency[key] <= 1 {
				delete(s.concurrency, key)
			} else {
				s.concurrency[key]--
			}
		})
	}
	return release, nil
}

// ResolvedConnection holds parsed OpenAI-compatible provider credentials.
type ResolvedConnection struct {
	BaseURL       string
	DefaultModel  string
	AllowedModels []string
	APIKey        string
}

// ResolveConnection resolves the openai_compatible connection for an app.
func (s *Service) ResolveConnection(ctx context.Context, appID uuid.UUID) (ResolvedConnection, error) {
	conn, err := s.resolver.For(ctx, appID, integration.ProviderOpenAI)
	if errors.Is(err, integration.ErrNotConfigured) {
		return ResolvedConnection{}, ErrAINotEnabled.WithDetails(map[string]any{"reason": "openai_compatible connection not configured"})
	}
	if err != nil {
		return ResolvedConnection{}, err
	}
	var settings map[string]any
	_ = json.Unmarshal(conn.Settings, &settings)
	baseURL, _ := settings["base_url"].(string)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	defaultModel, _ := settings["default_model"].(string)
	var allowed []string
	if raw, ok := settings["allowed_models"].([]any); ok {
		for _, v := range raw {
			if str, ok := v.(string); ok && str != "" {
				allowed = append(allowed, str)
			}
		}
	}
	var sec integration.APIKeySecrets
	if err := json.Unmarshal(conn.Secrets, &sec); err != nil || sec.APIKey == "" {
		return ResolvedConnection{}, ErrAINotEnabled.WithDetails(map[string]any{"reason": "api_key missing in connection"})
	}
	return ResolvedConnection{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		DefaultModel:  defaultModel,
		AllowedModels: allowed,
		APIKey:        sec.APIKey,
	}, nil
}

// CalculateHoldCredits computes estimated maximum credits to pre-debit.
func (s *Service) CalculateHoldCredits(app tenant.App, model string, maxTokens int, promptChars int) int64 {
	pricing := app.Settings.AI.Pricing
	if pricing.Mode == "per_token" {
		modelPricing, ok := pricing.Models[model]
		if ok {
			estInputTokens := int64(promptChars / 4)
			if estInputTokens < 10 {
				estInputTokens = 10
			}
			inputCost := (estInputTokens * modelPricing.InputCreditsPer1K) / 1000
			outputCost := (int64(maxTokens) * modelPricing.OutputCreditsPer1K) / 1000
			total := inputCost + outputCost
			if total < modelPricing.MinCredits {
				total = modelPricing.MinCredits
			}
			if total < 1 {
				total = 1
			}
			return total
		}
	}
	// Default per_request
	if modelPricing, ok := pricing.Models[model]; ok && modelPricing.CreditsPerRequest > 0 {
		return modelPricing.CreditsPerRequest
	}
	if pricing.DefaultCreditsPerRequest > 0 {
		return pricing.DefaultCreditsPerRequest
	}
	return 1
}

// CalculateActualCredits calculates final credit cost from token usage.
func (s *Service) CalculateActualCredits(app tenant.App, model string, promptTokens, completionTokens int) int64 {
	pricing := app.Settings.AI.Pricing
	if pricing.Mode == "per_token" {
		modelPricing, ok := pricing.Models[model]
		if ok {
			inputCost := (int64(promptTokens) * modelPricing.InputCreditsPer1K) / 1000
			outputCost := (int64(completionTokens) * modelPricing.OutputCreditsPer1K) / 1000
			total := inputCost + outputCost
			if total < modelPricing.MinCredits {
				total = modelPricing.MinCredits
			}
			if total < 1 {
				total = 1
			}
			return total
		}
	}
	return s.CalculateHoldCredits(app, model, 0, 0)
}

// SettleRefund restores credits when upstream LLM fails without output.
func (s *Service) SettleRefund(ctx context.Context, appID, userID uuid.UUID, holdCredits int64, reqID string) error {
	if holdCredits <= 0 {
		return nil
	}
	key := "ai_settle:" + userID.String() + ":" + reqID
	_, err := s.credits.Apply(ctx, credit.ApplyInput{
		AppID:          appID,
		UserID:         userID,
		EntryType:      credit.TypeRefundReversal,
		Amount:         holdCredits,
		IdempotencyKey: key,
		ReferenceType:  "ai_request",
		ReferenceID:    reqID,
		Actor:          "system",
		Reason:         "ai upstream error refund",
	})
	return err
}

// SettleDelta handles token-based over/under settlement.
func (s *Service) SettleDelta(ctx context.Context, appID, userID uuid.UUID, holdCredits, actualCredits int64, reqID string) error {
	delta := holdCredits - actualCredits
	if delta == 0 {
		return nil
	}
	key := fmt.Sprintf("ai_settle:%s:%s:%d", userID.String(), reqID, actualCredits)
	if delta > 0 {
		// Refund extra credits
		_, err := s.credits.Apply(ctx, credit.ApplyInput{
			AppID:          appID,
			UserID:         userID,
			EntryType:      credit.TypeRefundReversal,
			Amount:         delta,
			IdempotencyKey: key,
			ReferenceType:  "ai_request",
			ReferenceID:    reqID,
			Actor:          "system",
			Reason:         "ai token adjustment refund",
		})
		return err
	}
	// Additional charge
	_, err := s.credits.Apply(ctx, credit.ApplyInput{
		AppID:          appID,
		UserID:         userID,
		EntryType:      credit.TypeRefund,
		Amount:         delta, // negative
		IdempotencyKey: key,
		ReferenceType:  "ai_request",
		ReferenceID:    reqID,
		Actor:          "system",
		Reason:         "ai token adjustment charge",
	})
	return err
}

// ForwardRequest sends the prepared payload to upstream LLM.
func (s *Service) ForwardRequest(ctx context.Context, conn ResolvedConnection, body []byte) (*http.Response, error) {
	url := conn.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+conn.APIKey)
	return s.http.Do(req)
}

// TestConnection sends a test 1-token completion to verify API key & endpoint.
func (s *Service) TestConnection(ctx context.Context, conn ResolvedConnection) (string, error) {
	model := conn.DefaultModel
	if model == "" && len(conn.AllowedModels) > 0 {
		model = conn.AllowedModels[0]
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 1,
	})
	res, err := s.ForwardRequest(ctx, conn, payload)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream error (%d): %s", res.StatusCode, string(respBody))
	}
	return "ok", nil
}

// RenderTemplate replaces {{{VAR}}} placeholders with provided variables.
func RenderTemplate(template string, vars map[string]any) string {
	res := template
	for k, v := range vars {
		val := fmt.Sprintf("%v", v)
		res = strings.ReplaceAll(res, "{{{"+k+"}}}", val)
	}
	return res
}
