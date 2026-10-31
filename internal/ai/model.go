package ai

import (
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Preset is a reusable server-side AI prompt template.
type Preset struct {
	ID                 uuid.UUID `db:"id" json:"id"`
	AppID              uuid.UUID `db:"app_id" json:"app_id"`
	Slug               string    `db:"slug" json:"slug"`
	Name               string    `db:"name" json:"name"`
	Description        string    `db:"description" json:"description"`
	Model              string    `db:"model" json:"model"`
	SystemPrompt       string    `db:"system_prompt" json:"system_prompt"`
	UserPromptTemplate string    `db:"user_prompt_template" json:"user_prompt_template"`
	Temperature        float64   `db:"temperature" json:"temperature"`
	MaxTokens          int       `db:"max_tokens" json:"max_tokens"`
	ResponseFormat     string    `db:"response_format" json:"response_format"` // text | json_object
	CreditsCost        int64     `db:"credits_cost" json:"credits_cost"`
	Published          bool      `db:"published" json:"published"`
	CreatedAt          db.Time   `db:"created_at" json:"created_at"`
	UpdatedAt          db.Time   `db:"updated_at" json:"updated_at"`
}

// PresetInput is the payload for creating or updating a preset.
type PresetInput struct {
	Slug               string   `json:"slug"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Model              string   `json:"model"`
	SystemPrompt       string   `json:"system_prompt"`
	UserPromptTemplate string   `json:"user_prompt_template"`
	Temperature        *float64 `json:"temperature"`
	MaxTokens          *int     `json:"max_tokens"`
	ResponseFormat     string   `json:"response_format"`
	CreditsCost        *int64   `json:"credits_cost"`
	Published          *bool    `json:"published"`
}

// ChatMessage represents a single message in an OpenAI chat request.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ChatRequest is the standard OpenAI-compatible chat completion payload.
type ChatRequest struct {
	Model          string         `json:"model"`
	Messages       []ChatMessage  `json:"messages"`
	Temperature    *float64       `json:"temperature,omitempty"`
	MaxTokens      *int           `json:"max_tokens,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	Extra          map[string]any `json:"-"`
}

// RunPresetRequest is the request body for running a preset.
type RunPresetRequest struct {
	Variables map[string]any `json:"variables"`
	Stream    bool           `json:"stream"`
}
