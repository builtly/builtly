package email

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists per-app templates, trigger bindings, and custom-send idempotency keys.
type Store struct{ db *db.DB }

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store { return &Store{db: database} }

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

type templateRow struct {
	ID        uuid.UUID `db:"id"`
	AppID     uuid.UUID `db:"app_id"`
	Key       string    `db:"key"`
	Name      string    `db:"name"`
	Subject   string    `db:"subject"`
	HTML      string    `db:"html"`
	Text      string    `db:"text"`
	Variables []byte    `db:"variables"`
	Published bool      `db:"published"`
	CreatedAt db.Time   `db:"created_at"`
	UpdatedAt db.Time   `db:"updated_at"`
}

func (r templateRow) toTemplate() (Template, error) {
	vars := []Variable{}
	if len(r.Variables) > 0 && string(r.Variables) != "null" {
		if err := json.Unmarshal(r.Variables, &vars); err != nil {
			return Template{}, err
		}
	}
	return Template{
		ID: r.ID, AppID: r.AppID, Key: r.Key, Name: r.Name, Subject: r.Subject,
		HTML: r.HTML, Text: r.Text, Variables: vars, Published: r.Published,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

const selectTemplate = `SELECT id, app_id, key, name, subject, html, text, variables, published, created_at, updated_at FROM email_templates`

// List returns templates for an app, oldest first.
func (s *Store) List(ctx context.Context, q db.Querier, appID uuid.UUID) ([]Template, error) {
	var rows []templateRow
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectTemplate+` WHERE app_id = ? ORDER BY created_at, id`), appID)
	if err != nil {
		return nil, db.MapError(err)
	}
	out := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := r.toTemplate()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Get loads one template.
func (s *Store) Get(ctx context.Context, q db.Querier, appID, id uuid.UUID) (Template, error) {
	var r templateRow
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectTemplate+` WHERE app_id = ? AND id = ?`), appID, id)
	if err != nil {
		return Template{}, db.MapError(err)
	}
	t, err := r.toTemplate()
	if err != nil {
		return Template{}, err
	}
	return t, nil
}

// GetByKey loads a template by alias.
func (s *Store) GetByKey(ctx context.Context, q db.Querier, appID uuid.UUID, key string) (Template, error) {
	var r templateRow
	err := s.q(q).GetContext(ctx, &r, s.db.Rebind(selectTemplate+` WHERE app_id = ? AND key = ?`), appID, key)
	if err != nil {
		return Template{}, db.MapError(err)
	}
	return r.toTemplate()
}

// Insert creates a template.
func (s *Store) Insert(ctx context.Context, q db.Querier, t Template) error {
	raw, err := json.Marshal(t.Variables)
	if err != nil {
		return err
	}
	_, err = s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO email_templates (
			id, app_id, key, name, subject, html, text, variables, published, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		t.ID, t.AppID, t.Key, t.Name, t.Subject, t.HTML, t.Text, raw, s.db.BoolArg(t.Published), t.CreatedAt, t.UpdatedAt)
	return db.MapError(err)
}

// Update replaces editable fields.
func (s *Store) Update(ctx context.Context, q db.Querier, t Template) error {
	raw, err := json.Marshal(t.Variables)
	if err != nil {
		return err
	}
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE email_templates
		SET key = ?, name = ?, subject = ?, html = ?, text = ?, variables = ?, published = ?, updated_at = ?
		WHERE app_id = ? AND id = ?`),
		t.Key, t.Name, t.Subject, t.HTML, t.Text, raw, s.db.BoolArg(t.Published), t.UpdatedAt, t.AppID, t.ID)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// Delete removes a template.
func (s *Store) Delete(ctx context.Context, q db.Querier, appID, id uuid.UUID) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`DELETE FROM email_templates WHERE app_id = ? AND id = ?`), appID, id)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// ListBindings returns trigger overrides for an app.
func (s *Store) ListBindings(ctx context.Context, q db.Querier, appID uuid.UUID) ([]Binding, error) {
	var rows []Binding
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT app_id, trigger, template_id, updated_at FROM email_trigger_bindings WHERE app_id = ?`), appID)
	return rows, db.MapError(err)
}

// GetBinding loads one trigger binding.
func (s *Store) GetBinding(ctx context.Context, q db.Querier, appID uuid.UUID, trigger string) (Binding, error) {
	var b Binding
	err := s.q(q).GetContext(ctx, &b, s.db.Rebind(`
		SELECT app_id, trigger, template_id, updated_at FROM email_trigger_bindings
		WHERE app_id = ? AND trigger = ?`), appID, trigger)
	if err != nil {
		return Binding{}, db.MapError(err)
	}
	return b, nil
}

// BindingForTemplate reports whether a template is used by any trigger.
func (s *Store) BindingForTemplate(ctx context.Context, q db.Querier, appID, templateID uuid.UUID) (Binding, error) {
	var b Binding
	err := s.q(q).GetContext(ctx, &b, s.db.Rebind(`
		SELECT app_id, trigger, template_id, updated_at FROM email_trigger_bindings
		WHERE app_id = ? AND template_id = ?`), appID, templateID)
	if err != nil {
		return Binding{}, db.MapError(err)
	}
	return b, nil
}

// UpsertBinding sets or replaces a trigger's template.
func (s *Store) UpsertBinding(ctx context.Context, q db.Querier, b Binding) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO email_trigger_bindings (app_id, trigger, template_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (app_id, trigger) DO UPDATE SET template_id = excluded.template_id, updated_at = excluded.updated_at`),
		b.AppID, b.Trigger, b.TemplateID, b.UpdatedAt)
	return db.MapError(err)
}

// DeleteBinding removes a trigger override.
func (s *Store) DeleteBinding(ctx context.Context, q db.Querier, appID uuid.UUID, trigger string) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		DELETE FROM email_trigger_bindings WHERE app_id = ? AND trigger = ?`), appID, trigger)
	return db.MapError(err)
}

// InsertSend records a custom send. Unique (app_id, idempotency_key).
func (s *Store) InsertSend(ctx context.Context, q db.Querier, row Send) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO email_sends (app_id, idempotency_key, template_key, variables_hash, outbox_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`),
		row.AppID, row.IdempotencyKey, row.TemplateKey, row.VariablesHash, row.OutboxID, row.CreatedAt)
	return db.MapError(err)
}

// GetSend loads an idempotent custom send.
func (s *Store) GetSend(ctx context.Context, q db.Querier, appID uuid.UUID, key string) (Send, error) {
	var row Send
	err := s.q(q).GetContext(ctx, &row, s.db.Rebind(`
		SELECT app_id, idempotency_key, template_key, variables_hash, outbox_id, created_at
		FROM email_sends WHERE app_id = ? AND idempotency_key = ?`), appID, key)
	if err != nil {
		return Send{}, db.MapError(err)
	}
	return row, nil
}
