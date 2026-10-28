package integration

import (
	"context"
	"database/sql"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists connections and bindings.
type Store struct {
	db *db.DB
}

// NewStore constructs a Store.
func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) q(q db.Querier) db.Querier {
	if q != nil {
		return q
	}
	return s.db
}

const selectConn = `SELECT id, provider, name, settings, secrets_enc, key_version, created_at, updated_at FROM connections`

// InsertConnection inserts a connection row.
func (s *Store) InsertConnection(ctx context.Context, q db.Querier, c Connection) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO connections (id, provider, name, settings, secrets_enc, key_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		c.ID, c.Provider, c.Name, c.Settings, c.SecretsEnc, c.KeyVersion, c.CreatedAt, c.UpdatedAt)
	return db.MapError(err)
}

// UpdateConnection updates mutable connection fields.
func (s *Store) UpdateConnection(ctx context.Context, q db.Querier, c Connection) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE connections SET name = ?, settings = ?, secrets_enc = ?, key_version = ?, updated_at = ?
		WHERE id = ?`),
		c.Name, c.Settings, c.SecretsEnc, c.KeyVersion, c.UpdatedAt, c.ID)
	return db.MapError(err)
}

// GetConnection loads by id.
func (s *Store) GetConnection(ctx context.Context, q db.Querier, id string) (Connection, error) {
	var c Connection
	err := s.q(q).GetContext(ctx, &c, s.db.Rebind(selectConn+` WHERE id = ?`), id)
	if err != nil {
		return Connection{}, db.MapError(err)
	}
	return c, nil
}

// ListConnections lists connections, optionally filtered by provider.
func (s *Store) ListConnections(ctx context.Context, q db.Querier, provider string) ([]Connection, error) {
	var rows []Connection
	var err error
	if provider == "" {
		err = s.q(q).SelectContext(ctx, &rows, selectConn+` ORDER BY provider, created_at`)
	} else {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectConn+` WHERE provider = ? ORDER BY created_at`), provider)
	}
	return rows, db.MapError(err)
}

// DeleteConnection deletes a connection by id.
func (s *Store) DeleteConnection(ctx context.Context, q db.Querier, id string) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`DELETE FROM connections WHERE id = ?`), id)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// ListBindingsForConnection returns apps bound to a connection.
func (s *Store) ListBindingsForConnection(ctx context.Context, q db.Querier, connectionID string) ([]Binding, error) {
	var rows []Binding
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT app_id, provider, connection_id, settings, created_at, updated_at
		FROM app_integrations WHERE connection_id = ?`), connectionID)
	return rows, db.MapError(err)
}

// ListBindingsForApp returns all bindings for an app.
func (s *Store) ListBindingsForApp(ctx context.Context, q db.Querier, appID uuid.UUID) ([]Binding, error) {
	var rows []Binding
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT app_id, provider, connection_id, settings, created_at, updated_at
		FROM app_integrations WHERE app_id = ?`), appID)
	return rows, db.MapError(err)
}

// GetBinding returns the binding for (app, provider).
func (s *Store) GetBinding(ctx context.Context, q db.Querier, appID uuid.UUID, provider string) (Binding, error) {
	var b Binding
	err := s.q(q).GetContext(ctx, &b, s.db.Rebind(`
		SELECT app_id, provider, connection_id, settings, created_at, updated_at
		FROM app_integrations WHERE app_id = ? AND provider = ?`), appID, provider)
	if err != nil {
		return Binding{}, db.MapError(err)
	}
	return b, nil
}

// UpsertBinding inserts or replaces a binding.
func (s *Store) UpsertBinding(ctx context.Context, q db.Querier, b Binding) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO app_integrations (app_id, provider, connection_id, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, provider) DO UPDATE SET
			connection_id = excluded.connection_id,
			settings = excluded.settings,
			updated_at = excluded.updated_at`),
		b.AppID, b.Provider, b.ConnectionID, b.Settings, b.CreatedAt, b.UpdatedAt)
	return db.MapError(err)
}

// DeleteBinding removes a binding.
func (s *Store) DeleteBinding(ctx context.Context, q db.Querier, appID uuid.UUID, provider string) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		DELETE FROM app_integrations WHERE app_id = ? AND provider = ?`), appID, provider)
	return db.MapError(err)
}

// DeleteBindingsForApp removes all bindings for an app (used when replacing config).
func (s *Store) DeleteBindingsForApp(ctx context.Context, q db.Querier, appID uuid.UUID) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`DELETE FROM app_integrations WHERE app_id = ?`), appID)
	return db.MapError(err)
}

// FindExclusiveBinding returns the app currently holding an exclusive binding to connectionID
// (or the env default when connectionID is empty / NULL).
func (s *Store) FindExclusiveBinding(ctx context.Context, q db.Querier, provider string, connectionID *string) (Binding, error) {
	var b Binding
	var err error
	if connectionID == nil {
		err = s.q(q).GetContext(ctx, &b, s.db.Rebind(`
			SELECT app_id, provider, connection_id, settings, created_at, updated_at
			FROM app_integrations WHERE provider = ? AND connection_id IS NULL`), provider)
	} else {
		err = s.q(q).GetContext(ctx, &b, s.db.Rebind(`
			SELECT app_id, provider, connection_id, settings, created_at, updated_at
			FROM app_integrations WHERE provider = ? AND connection_id = ?`), provider, *connectionID)
	}
	if err != nil {
		return Binding{}, db.MapError(err)
	}
	return b, nil
}

// CountBindings returns how many apps use a connection.
func (s *Store) CountBindings(ctx context.Context, q db.Querier, connectionID string) (int, error) {
	var n int
	err := s.q(q).GetContext(ctx, &n, s.db.Rebind(`
		SELECT COUNT(*) FROM app_integrations WHERE connection_id = ?`), connectionID)
	return n, db.MapError(err)
}

var _ = sql.ErrNoRows
