package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

var (
	featureRe = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
	slugRe    = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
)

// NewConsumeHook returns a credit.ConsumeHook that publishes credits.consumed when enabled on the app.
func NewConsumeHook(tenants *tenant.Service, pub *outbox.Publisher) credit.ConsumeHook {
	return func(ctx context.Context, tx *sqlx.Tx, entry credit.Entry, feature string) error {
		if tenants == nil || pub == nil {
			return nil
		}
		app, err := tenants.GetTx(ctx, tx, entry.AppID)
		if err != nil || !app.Settings.Analytics.CaptureConsumeEvents {
			return nil
		}
		payload, err := json.Marshal(map[string]any{
			"user_id":         entry.UserID.String(),
			"ledger_entry_id": entry.ID.String(),
			"amount":          -entry.Amount,
			"feature":         feature,
		})
		if err != nil {
			return err
		}
		appID := entry.AppID
		return pub.PublishTx(ctx, tx, outbox.Event{
			AppID: &appID, Type: outbox.EventCreditsConsumed, Payload: payload,
		})
	}
}

// Handler serves AI endpoints (gateway, presets, legacy consume).
type Handler struct {
	svc     *Service
	credits *credit.Service
	users   *auth.Store
}

// NewHandler constructs an AI Handler.
func NewHandler(credits *credit.Service, users *auth.Store) *Handler {
	return &Handler{credits: credits, users: users}
}

// SetService attaches the AI Service for gateway and preset execution.
func (h *Handler) SetService(svc *Service) {
	h.svc = svc
}

// Consume handles legacy POST /ai/consume.
func (h *Handler) Consume(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		httpx.WriteError(w, r, credit.ErrMissingIdempotency)
		return
	}

	var body struct {
		UserID   *uuid.UUID      `json:"user_id"`
		Amount   int64           `json:"amount"`
		Feature  string          `json:"feature"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !featureRe.MatchString(body.Feature) {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid feature"}))
		return
	}
	if len(body.Metadata) > 4<<10 {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "metadata too large"}))
		return
	}

	var userID uuid.UUID
	if p.IsAPIKey {
		if !p.HasScope("credits:consume") {
			httpx.WriteError(w, r, apperr.Forbidden.WithDetails(map[string]any{"reason": "missing scope credits:consume"}))
			return
		}
		if body.UserID == nil {
			httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "user_id required with API key"}))
			return
		}
		userID = *body.UserID
	} else {
		if body.UserID != nil {
			httpx.WriteError(w, r, apperr.Forbidden.WithDetails(map[string]any{"reason": "user_id forbidden with user JWT"}))
			return
		}
		userID = p.UserID
	}
	if h.users != nil {
		u, err := h.users.GetByID(r.Context(), nil, app.ID, userID)
		if err != nil || u.Status == auth.StatusDeleted {
			httpx.WriteError(w, r, auth.ErrUserNotFound)
			return
		}
		if u.Status == auth.StatusDisabled {
			httpx.WriteError(w, r, auth.ErrAccountDisabled)
			return
		}
	}

	res, err := h.credits.Consume(r.Context(), app.ID, userID, body.Amount, idem, body.Feature, p.Actor, body.Metadata)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ledger_entry_id": res.Entry.ID,
		"amount":          body.Amount,
		"balance":         res.Balance,
		"replayed":        res.Replayed,
	})
}

// ChatCompletions handles POST /api/v1/ai/chat/completions.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	if !app.Settings.AI.Enabled {
		httpx.WriteError(w, r, ErrAINotEnabled)
		return
	}
	if !app.Settings.AI.AllowDirectChat {
		httpx.WriteError(w, r, ErrDirectChatDisabled)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	userID, err := h.resolveUserID(r.Context(), app.ID, p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	conn, err := h.svc.ResolveConnection(r.Context(), app.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2 MB limit
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	var req ChatRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid JSON"}))
		return
	}
	if req.Model == "" {
		req.Model = conn.DefaultModel
	}
	if len(conn.AllowedModels) > 0 && !containsString(conn.AllowedModels, req.Model) {
		httpx.WriteError(w, r, ErrModelNotAllowed)
		return
	}
	if len(req.Messages) == 0 {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "messages required"}))
		return
	}
	if app.Settings.AI.MaxContextMessages > 0 && len(req.Messages) > app.Settings.AI.MaxContextMessages {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "too many messages"}))
		return
	}

	maxTokens := app.Settings.AI.DefaultMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}
	h.proxyChat(w, r, app, userID, p.Actor, conn, req, bodyBytes, maxTokens, "")
}

// RunPreset handles POST /api/v1/ai/presets/{slug}/run.
func (h *Handler) RunPreset(w http.ResponseWriter, r *http.Request) {
	app, _ := tenant.FromContext(r.Context())
	if !app.Settings.AI.Enabled {
		httpx.WriteError(w, r, ErrAINotEnabled)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	userID, err := h.resolveUserID(r.Context(), app.ID, p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	slug := chi.URLParam(r, "slug")
	preset, err := h.svc.store.GetPresetBySlug(r.Context(), nil, app.ID, slug)
	if err != nil || !preset.Published {
		httpx.WriteError(w, r, ErrPresetNotFound)
		return
	}

	conn, err := h.svc.ResolveConnection(r.Context(), app.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	var runReq RunPresetRequest
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &runReq); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if runReq.Variables == nil {
		runReq.Variables = map[string]any{}
	}

	// Render messages
	messages := make([]ChatMessage, 0, 2)
	if preset.SystemPrompt != "" {
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: RenderTemplate(preset.SystemPrompt, runReq.Variables),
		})
	}
	userPrompt := RenderTemplate(preset.UserPromptTemplate, runReq.Variables)
	if userPrompt != "" {
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: userPrompt,
		})
	} else if input, ok := runReq.Variables["INPUT"].(string); ok && input != "" {
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: input,
		})
	}

	temp := preset.Temperature
	maxTok := preset.MaxTokens
	req := ChatRequest{
		Model:       preset.Model,
		Messages:    messages,
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Stream:      runReq.Stream,
	}
	if preset.ResponseFormat == "json_object" {
		req.ResponseFormat = map[string]any{"type": "json_object"}
	}
	fwdBytes, _ := json.Marshal(req)
	h.proxyChat(w, r, app, userID, p.Actor, conn, req, fwdBytes, maxTok, preset.Slug)
}

func (h *Handler) proxyChat(
	w http.ResponseWriter,
	r *http.Request,
	app tenant.App,
	userID uuid.UUID,
	actor string,
	conn ResolvedConnection,
	req ChatRequest,
	fwdBytes []byte,
	maxTokens int,
	presetSlug string,
) {
	// Concurrency control
	release, err := h.svc.AcquireConcurrency(app.ID, userID, app.Settings.AI.MaxConcurrentPerUser)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer release()

	// Pre-debit credits
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m.Content)
	}
	holdCredits := h.svc.CalculateHoldCredits(app, req.Model, maxTokens, promptChars)
	reqID := r.Header.Get("Idempotency-Key")
	if reqID == "" {
		reqID = uuid.Must(uuid.NewV7()).String()
	}
	featureName := "ai.chat"
	if presetSlug != "" {
		featureName = "ai.preset." + presetSlug
	}

	meta, _ := json.Marshal(map[string]any{
		"model":  req.Model,
		"preset": presetSlug,
		"hold":   holdCredits,
	})
	consumeRes, err := h.credits.Consume(r.Context(), app.ID, userID, holdCredits, reqID, featureName, actor, meta)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	// Prepare upstream payload (stream_options to get usage in streams)
	if req.Stream {
		var rawMap map[string]any
		_ = json.Unmarshal(fwdBytes, &rawMap)
		if rawMap == nil {
			rawMap = map[string]any{}
		}
		rawMap["stream"] = true
		rawMap["stream_options"] = map[string]any{"include_usage": true}
		fwdBytes, _ = json.Marshal(rawMap)
	}

	// Send to upstream
	upstreamRes, err := h.svc.ForwardRequest(r.Context(), conn, fwdBytes)
	if err != nil {
		_ = h.svc.SettleRefund(r.Context(), app.ID, userID, holdCredits, reqID)
		httpx.WriteError(w, r, ErrUpstreamError.WithDetails(map[string]any{"reason": err.Error()}))
		return
	}
	defer upstreamRes.Body.Close()

	if upstreamRes.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(upstreamRes.Body, 8<<10))
		_ = h.svc.SettleRefund(r.Context(), app.ID, userID, holdCredits, reqID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstreamRes.StatusCode)
		_, _ = w.Write(respBody)
		return
	}

	// Stream vs non-stream
	if req.Stream {
		h.handleStream(w, r, upstreamRes, app, userID, reqID, req.Model, holdCredits)
	} else {
		h.handleNonStream(w, upstreamRes, app, userID, reqID, req.Model, holdCredits, consumeRes.Balance)
	}
}

func (h *Handler) handleNonStream(
	w http.ResponseWriter,
	upstreamRes *http.Response,
	app tenant.App,
	userID uuid.UUID,
	reqID string,
	model string,
	holdCredits int64,
	balanceRemaining int64,
) {
	respBody, err := io.ReadAll(io.LimitReader(upstreamRes.Body, 4<<20))
	if err != nil {
		_ = h.svc.SettleRefund(context.Background(), app.ID, userID, holdCredits, reqID)
		httpx.WriteError(w, nil, ErrUpstreamError)
		return
	}

	// Parse usage
	var out struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &out)
	actualCredits := h.svc.CalculateActualCredits(app, model, out.Usage.PromptTokens, out.Usage.CompletionTokens)
	_ = h.svc.SettleDelta(context.Background(), app.ID, userID, holdCredits, actualCredits, reqID)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Builtly-Credits-Charged", strconv.FormatInt(actualCredits, 10))
	w.Header().Set("X-Builtly-Balance-Remaining", strconv.FormatInt(balanceRemaining+(holdCredits-actualCredits), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBody)
}

func (h *Handler) handleStream(
	w http.ResponseWriter,
	r *http.Request,
	upstreamRes *http.Response,
	app tenant.App,
	userID uuid.UUID,
	reqID string,
	model string,
	holdCredits int64,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		_ = h.svc.SettleRefund(context.Background(), app.ID, userID, holdCredits, reqID)
		httpx.WriteError(w, r, apperr.Internal)
		return
	}

	// Extend write timeout for long streaming responses
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	scanner := bufio.NewScanner(upstreamRes.Body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var promptTokens, completionTokens int
	var emittedChars int
	seenUsage := false

	for scanner.Scan() {
		line := scanner.Bytes()
		_, _ = w.Write(line)
		_, _ = w.Write([]byte("\n"))
		flusher.Flush()

		if bytes.HasPrefix(line, []byte("data: ")) {
			payload := bytes.TrimPrefix(line, []byte("data: "))
			if !bytes.Equal(payload, []byte("[DONE]")) {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
					Usage *struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(payload, &chunk); err == nil {
					for _, c := range chunk.Choices {
						emittedChars += len(c.Delta.Content)
					}
					if chunk.Usage != nil {
						promptTokens = chunk.Usage.PromptTokens
						completionTokens = chunk.Usage.CompletionTokens
						seenUsage = true
					}
				}
			}
		}
	}

	if !seenUsage && emittedChars > 0 {
		completionTokens = emittedChars / 4
		if completionTokens < 1 {
			completionTokens = 1
		}
	}

	actualCredits := h.svc.CalculateActualCredits(app, model, promptTokens, completionTokens)
	_ = h.svc.SettleDelta(context.Background(), app.ID, userID, holdCredits, actualCredits, reqID)
}

func (h *Handler) resolveUserID(ctx context.Context, appID uuid.UUID, p principal.Principal) (uuid.UUID, error) {
	if p.IsAPIKey {
		if !p.HasScope("credits:consume") {
			return uuid.Nil, apperr.Forbidden.WithDetails(map[string]any{"reason": "missing scope credits:consume"})
		}
		return uuid.Nil, apperr.BadRequest.WithDetails(map[string]any{"reason": "user_id required in API key context"})
	}
	if h.users != nil {
		u, err := h.users.GetByID(ctx, nil, appID, p.UserID)
		if err != nil || u.Status == auth.StatusDeleted {
			return uuid.Nil, auth.ErrUserNotFound
		}
		if u.Status == auth.StatusDisabled {
			return uuid.Nil, auth.ErrAccountDisabled
		}
	}
	return p.UserID, nil
}

// Preset management handlers for Admin.

func (h *Handler) ListAdminPresets(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	items, err := h.svc.store.ListPresets(r.Context(), nil, appID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) CreateAdminPreset(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	var in PresetInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in.Slug = strings.TrimSpace(in.Slug)
	if !slugRe.MatchString(in.Slug) {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid slug"}))
		return
	}
	if in.Name == "" || in.Model == "" {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "name and model required"}))
		return
	}
	temp := 0.7
	if in.Temperature != nil {
		temp = *in.Temperature
	}
	maxTok := 2048
	if in.MaxTokens != nil && *in.MaxTokens > 0 {
		maxTok = *in.MaxTokens
	}
	cost := int64(1)
	if in.CreditsCost != nil && *in.CreditsCost >= 0 {
		cost = *in.CreditsCost
	}
	pub := true
	if in.Published != nil {
		pub = *in.Published
	}
	now := db.Now()
	p := Preset{
		ID:                 uuid.Must(uuid.NewV7()),
		AppID:              appID,
		Slug:               in.Slug,
		Name:               in.Name,
		Description:        in.Description,
		Model:              in.Model,
		SystemPrompt:       in.SystemPrompt,
		UserPromptTemplate: in.UserPromptTemplate,
		Temperature:        temp,
		MaxTokens:          maxTok,
		ResponseFormat:     in.ResponseFormat,
		CreditsCost:        cost,
		Published:          pub,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := h.svc.store.InsertPreset(r.Context(), nil, p); err != nil {
		var uv db.ErrUniqueViolation
		if errors.As(err, &uv) {
			httpx.WriteError(w, r, ErrPresetSlugTaken)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (h *Handler) PatchAdminPreset(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	p, err := h.svc.store.GetPresetByID(r.Context(), nil, appID, id)
	if err != nil {
		httpx.WriteError(w, r, ErrPresetNotFound)
		return
	}
	var in PresetInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.Slug != "" && slugRe.MatchString(in.Slug) {
		p.Slug = in.Slug
	}
	if in.Name != "" {
		p.Name = in.Name
	}
	p.Description = in.Description
	if in.Model != "" {
		p.Model = in.Model
	}
	p.SystemPrompt = in.SystemPrompt
	p.UserPromptTemplate = in.UserPromptTemplate
	if in.Temperature != nil {
		p.Temperature = *in.Temperature
	}
	if in.MaxTokens != nil && *in.MaxTokens > 0 {
		p.MaxTokens = *in.MaxTokens
	}
	if in.ResponseFormat != "" {
		p.ResponseFormat = in.ResponseFormat
	}
	if in.CreditsCost != nil && *in.CreditsCost >= 0 {
		p.CreditsCost = *in.CreditsCost
	}
	if in.Published != nil {
		p.Published = *in.Published
	}
	p.UpdatedAt = db.Now()
	if err := h.svc.store.UpdatePreset(r.Context(), nil, p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) DeleteAdminPreset(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest)
		return
	}
	if err := h.svc.store.DeletePreset(r.Context(), nil, appID, id); err != nil {
		httpx.WriteError(w, r, ErrPresetNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestAdminLLM tests the OpenAI-compatible connection.
func (h *Handler) TestAdminLLM(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connection_id")
	if connID == "" {
		connID = chi.URLParam(r, "id")
	}
	conn, err := h.svc.resolver.OpenConnectionDirect(r.Context(), connID, integration.ProviderOpenAI)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var settings map[string]any
	_ = json.Unmarshal(conn.Settings, &settings)
	baseURL, _ := settings["base_url"].(string)
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	var sec integration.APIKeySecrets
	_ = json.Unmarshal(conn.Secrets, &sec)

	status, err := h.svc.TestConnection(r.Context(), ResolvedConnection{
		BaseURL:       baseURL,
		DefaultModel:  str(settings["default_model"]),
		AllowedModels: []string{},
		APIKey:        sec.APIKey,
	})
	if err != nil {
		logx.From(r.Context()).Warn("test llm failed", "err", err)
		httpx.WriteError(w, r, ErrUpstreamError.WithDetails(map[string]any{"reason": err.Error()}))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": status})
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
