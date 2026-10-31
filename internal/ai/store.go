package ai

import (
	"context"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists AI presets.
type Store struct {
	db *db.DB
}

// NewStore constructs an AI Store.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectPreset = `SELECT id, app_id, slug, name, description, model, system_prompt, user_prompt_template,
	temperature, max_tokens, response_format, credits_cost, published, created_at, updated_at FROM ai_presets`

// ListPresets returns all presets for an app, newest first.
func (s *Store) ListPresets(ctx context.Context, q db.Querier, appID uuid.UUID) ([]Preset, error) {
	var rows []Preset
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectPreset+` WHERE app_id = ? ORDER BY created_at DESC`), appID)
	if err != nil {
		return nil, db.MapError(err)
	}
	return rows, nil
}

// GetPresetByID loads a preset by ID.
func (s *Store) GetPresetByID(ctx context.Context, q db.Querier, appID, id uuid.UUID) (Preset, error) {
	var p Preset
	err := s.q(q).GetContext(ctx, &p, s.db.Rebind(selectPreset+` WHERE app_id = ? AND id = ?`), appID, id)
	if err != nil {
		return Preset{}, db.MapError(err)
	}
	return p, nil
}

// GetPresetBySlug loads a preset by slug.
func (s *Store) GetPresetBySlug(ctx context.Context, q db.Querier, appID uuid.UUID, slug string) (Preset, error) {
	var p Preset
	err := s.q(q).GetContext(ctx, &p, s.db.Rebind(selectPreset+` WHERE app_id = ? AND slug = ?`), appID, slug)
	if err != nil {
		return Preset{}, db.MapError(err)
	}
	return p, nil
}

// InsertPreset creates a new preset.
func (s *Store) InsertPreset(ctx context.Context, q db.Querier, p Preset) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO ai_presets (
			id, app_id, slug, name, description, model, system_prompt, user_prompt_template,
			temperature, max_tokens, response_format, credits_cost, published, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		p.ID, p.AppID, p.Slug, p.Name, p.Description, p.Model, p.SystemPrompt, p.UserPromptTemplate,
		p.Temperature, p.MaxTokens, p.ResponseFormat, p.CreditsCost, s.db.BoolArg(p.Published), p.CreatedAt, p.UpdatedAt)
	return db.MapError(err)
}

// UpdatePreset updates an existing preset.
func (s *Store) UpdatePreset(ctx context.Context, q db.Querier, p Preset) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE ai_presets SET
			slug = ?, name = ?, description = ?, model = ?, system_prompt = ?, user_prompt_template = ?,
			temperature = ?, max_tokens = ?, response_format = ?, credits_cost = ?, published = ?, updated_at = ?
		WHERE app_id = ? AND id = ?`),
		p.Slug, p.Name, p.Description, p.Model, p.SystemPrompt, p.UserPromptTemplate,
		p.Temperature, p.MaxTokens, p.ResponseFormat, p.CreditsCost, s.db.BoolArg(p.Published), p.UpdatedAt,
		p.AppID, p.ID)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// DeletePreset removes a preset.
func (s *Store) DeletePreset(ctx context.Context, q db.Querier, appID, id uuid.UUID) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`DELETE FROM ai_presets WHERE app_id = ? AND id = ?`), appID, id)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}
