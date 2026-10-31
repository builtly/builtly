package ai

import (
	"net/http"

	"github.com/builtly/builtly/internal/apperr"
)

var (
	ErrAINotEnabled       = apperr.New(http.StatusForbidden, "ai_disabled", "AI features are disabled for this app")
	ErrDirectChatDisabled = apperr.New(http.StatusForbidden, "direct_chat_disabled", "direct chat completions are disabled for this app")
	ErrPresetNotFound     = apperr.New(http.StatusNotFound, "preset_not_found", "AI preset not found")
	ErrPresetSlugTaken    = apperr.New(http.StatusConflict, "preset_slug_taken", "an AI preset with this slug already exists")
	ErrConcurrentLimit    = apperr.New(http.StatusTooManyRequests, "concurrent_limit_exceeded", "too many concurrent AI requests")
	ErrModelNotAllowed    = apperr.New(http.StatusForbidden, "model_not_allowed", "model is not allowed")
	ErrUpstreamError      = apperr.New(http.StatusBadGateway, "upstream_error", "AI provider request failed")
)
