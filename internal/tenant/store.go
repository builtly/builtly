package tenant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/platform/db"
)

// Store persists apps and API keys.
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

const selectApp = `SELECT id, slug, name, status, settings, created_at, updated_at FROM apps`

// GetByID loads an app by id.
func (s *Store) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (App, error) {
	var a App
	err := s.q(q).GetContext(ctx, &a, s.db.Rebind(selectApp+` WHERE id = ?`), id)
	if err != nil {
		return App{}, db.MapError(err)
	}
	if err := hydrateApp(&a); err != nil {
		return App{}, err
	}
	return a, nil
}

// GetBySlug loads an app by slug.
func (s *Store) GetBySlug(ctx context.Context, q db.Querier, slug string) (App, error) {
	var a App
	err := s.q(q).GetContext(ctx, &a, s.db.Rebind(selectApp+` WHERE slug = ?`), slug)
	if err != nil {
		return App{}, db.MapError(err)
	}
	if err := hydrateApp(&a); err != nil {
		return App{}, err
	}
	return a, nil
}

// ListActive returns all active apps (for CORS origin union).
func (s *Store) ListActive(ctx context.Context, q db.Querier) ([]App, error) {
	var rows []App
	err := s.q(q).SelectContext(ctx, &rows, selectApp+` WHERE status = 'active'`)
	if err != nil {
		return nil, db.MapError(err)
	}
	for i := range rows {
		if err := hydrateApp(&rows[i]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// List returns apps ordered by created_at desc with optional prefix filter on slug/name.
func (s *Store) List(ctx context.Context, q db.Querier, query string, limit int) ([]App, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows []App
		err  error
	)
	if query == "" {
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(selectApp+` ORDER BY created_at DESC LIMIT ?`), limit)
	} else {
		like := "%" + query + "%"
		err = s.q(q).SelectContext(ctx, &rows, s.db.Rebind(
			selectApp+` WHERE slug LIKE ? OR name LIKE ? ORDER BY created_at DESC LIMIT ?`),
			like, like, limit)
	}
	if err != nil {
		return nil, db.MapError(err)
	}
	for i := range rows {
		if err := hydrateApp(&rows[i]); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// Insert creates an app row.
func (s *Store) Insert(ctx context.Context, q db.Querier, a App) error {
	raw, err := a.Settings.Marshal()
	if err != nil {
		return err
	}
	_, err = s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO apps (id, slug, name, status, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`),
		a.ID, a.Slug, a.Name, a.Status, []byte(raw), a.CreatedAt, a.UpdatedAt)
	return db.MapError(err)
}

// UpdateMeta updates name and/or status.
func (s *Store) UpdateMeta(ctx context.Context, q db.Querier, id uuid.UUID, name, status string, updatedAt db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE apps SET name = ?, status = ?, updated_at = ? WHERE id = ?`),
		name, status, updatedAt, id)
	return db.MapError(err)
}

// UpdateSettings replaces apps.settings JSON.
func (s *Store) UpdateSettings(ctx context.Context, q db.Querier, id uuid.UUID, settings Settings, updatedAt db.Time) error {
	raw, err := settings.Marshal()
	if err != nil {
		return err
	}
	_, err = s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE apps SET settings = ?, updated_at = ? WHERE id = ?`),
		[]byte(raw), updatedAt, id)
	return db.MapError(err)
}

// InsertAPIKey inserts an API key row.
func (s *Store) InsertAPIKey(ctx context.Context, q db.Querier, k APIKey) error {
	scopes := k.Scopes
	if len(scopes) == 0 {
		var err error
		scopes, err = json.Marshal(DefaultScopes)
		if err != nil {
			return err
		}
	}
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		INSERT INTO app_api_keys (id, app_id, name, prefix, key_hash, scopes, last_used_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		k.ID, k.AppID, k.Name, k.Prefix, k.KeyHash, scopes, k.LastUsedAt, k.RevokedAt, k.CreatedAt)
	return db.MapError(err)
}

// ListAPIKeys lists keys for an app.
func (s *Store) ListAPIKeys(ctx context.Context, q db.Querier, appID uuid.UUID) ([]APIKey, error) {
	var rows []APIKey
	err := s.q(q).SelectContext(ctx, &rows, s.db.Rebind(`
		SELECT id, app_id, name, prefix, key_hash, scopes, last_used_at, revoked_at, created_at
		FROM app_api_keys WHERE app_id = ? ORDER BY created_at DESC`), appID)
	return rows, db.MapError(err)
}

// GetAPIKeyByHash looks up a non-revoked key by hash.
func (s *Store) GetAPIKeyByHash(ctx context.Context, q db.Querier, hash []byte) (APIKey, error) {
	var k APIKey
	err := s.q(q).GetContext(ctx, &k, s.db.Rebind(`
		SELECT id, app_id, name, prefix, key_hash, scopes, last_used_at, revoked_at, created_at
		FROM app_api_keys WHERE key_hash = ?`), hash)
	if err != nil {
		return APIKey{}, db.MapError(err)
	}
	return k, nil
}

// GetAPIKey returns a key by app and id.
func (s *Store) GetAPIKey(ctx context.Context, q db.Querier, appID, keyID uuid.UUID) (APIKey, error) {
	var k APIKey
	err := s.q(q).GetContext(ctx, &k, s.db.Rebind(`
		SELECT id, app_id, name, prefix, key_hash, scopes, last_used_at, revoked_at, created_at
		FROM app_api_keys WHERE app_id = ? AND id = ?`), appID, keyID)
	if err != nil {
		return APIKey{}, db.MapError(err)
	}
	return k, nil
}

// RevokeAPIKey sets revoked_at.
func (s *Store) RevokeAPIKey(ctx context.Context, q db.Querier, appID, keyID uuid.UUID, at db.Time) error {
	res, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE app_api_keys SET revoked_at = ? WHERE app_id = ? AND id = ? AND revoked_at IS NULL`),
		at, appID, keyID)
	if err != nil {
		return db.MapError(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// TouchAPIKeyLastUsed updates last_used_at at most once per minute (caller enforces).
func (s *Store) TouchAPIKeyLastUsed(ctx context.Context, q db.Querier, appID, keyID uuid.UUID, at db.Time) error {
	_, err := s.q(q).ExecContext(ctx, s.db.Rebind(`
		UPDATE app_api_keys SET last_used_at = ? WHERE app_id = ? AND id = ?`), at, appID, keyID)
	return db.MapError(err)
}

func hydrateApp(a *App) error {
	settings, err := ParseSettings(a.RawSettings)
	if err != nil {
		return err
	}
	a.Settings = settings
	return nil
}

// Ensure Querier works with *sqlx.Tx via db.DB methods — compile-time check.
var (
	_ db.Querier = (*sqlx.DB)(nil)
	_ db.Querier = (*sqlx.Tx)(nil)
	_            = sql.ErrNoRows
	_            = fmt.Errorf
)
