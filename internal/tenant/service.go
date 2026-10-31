package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)

// Service provides app lookup with a short TTL cache and API key helpers.
type Service struct {
	store *Store
	db    *db.DB

	mu    sync.RWMutex
	cache map[uuid.UUID]cacheEntry
	ttl   time.Duration
}

type cacheEntry struct {
	app App
	at  time.Time
}

// NewService constructs a tenant Service.
func NewService(database *db.DB, store *Store) *Service {
	return &Service{
		store: store,
		db:    database,
		cache: map[uuid.UUID]cacheEntry{},
		ttl:   30 * time.Second,
	}
}

// Get returns an app by ID, using the in-memory cache.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (App, error) {
	return s.GetTx(ctx, nil, id)
}

// GetTx returns an app by ID using the in-memory cache or the provided querier.
func (s *Service) GetTx(ctx context.Context, q db.Querier, id uuid.UUID) (App, error) {
	s.mu.RLock()
	if e, ok := s.cache[id]; ok && time.Since(e.at) < s.ttl {
		s.mu.RUnlock()
		return e.app, nil
	}
	s.mu.RUnlock()

	app, err := s.store.GetByID(ctx, q, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return App{}, ErrAppNotFound
		}
		return App{}, err
	}
	s.mu.Lock()
	s.cache[id] = cacheEntry{app: app, at: time.Now()}
	s.mu.Unlock()
	return app, nil
}

// Invalidate drops a cached app (and CORS consumers should rebuild).
func (s *Service) Invalidate(id uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
}

// InvalidateAll clears the app cache.
func (s *Service) InvalidateAll() {
	s.mu.Lock()
	s.cache = map[uuid.UUID]cacheEntry{}
	s.mu.Unlock()
}

// Create creates an app with defaults merged into settings.
func (s *Service) Create(ctx context.Context, slug, name string, partial Settings) (App, error) {
	settings, err := MergeSettings(DefaultSettings(), partial)
	if err != nil {
		return App{}, ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()})
	}
	return s.createWithResolvedSettings(ctx, slug, name, settings)
}

// CreateWithJSON creates an app by overlaying raw JSON settings onto DefaultSettings().
func (s *Service) CreateWithJSON(ctx context.Context, slug, name string, rawSettings json.RawMessage) (App, error) {
	settings, err := MergeSettingsJSON(DefaultSettings(), rawSettings)
	if err != nil {
		return App{}, ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()})
	}
	return s.createWithResolvedSettings(ctx, slug, name, settings)
}

func (s *Service) createWithResolvedSettings(ctx context.Context, slug, name string, settings Settings) (App, error) {
	if !slugRe.MatchString(slug) {
		return App{}, ErrInvalidSlug
	}
	if err := settings.Validate(); err != nil {
		return App{}, ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()})
	}
	now := db.Now()
	app := App{
		ID:        uuid.Must(uuid.NewV7()),
		Slug:      slug,
		Name:      name,
		Status:    StatusActive,
		Settings:  settings,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.Insert(ctx, nil, app); err != nil {
		var u db.ErrUniqueViolation
		if errors.As(err, &u) {
			return App{}, ErrSlugTaken
		}
		return App{}, err
	}
	return app, nil
}

// UpdateMeta patches name/status.
func (s *Service) UpdateMeta(ctx context.Context, id uuid.UUID, name *string, status *string) (App, error) {
	app, err := s.store.GetByID(ctx, nil, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return App{}, ErrAppNotFound
		}
		return App{}, err
	}
	if name != nil {
		app.Name = *name
	}
	if status != nil {
		switch *status {
		case StatusActive, StatusSuspended:
			app.Status = *status
		default:
			return App{}, ErrInvalidSettings.WithDetails(map[string]any{"reason": "invalid status"})
		}
	}
	app.UpdatedAt = db.Now()
	if err := s.store.UpdateMeta(ctx, nil, app.ID, app.Name, app.Status, app.UpdatedAt); err != nil {
		return App{}, err
	}
	s.Invalidate(id)
	return app, nil
}

// ReplaceSettings validates and stores settings.
func (s *Service) ReplaceSettings(ctx context.Context, id uuid.UUID, settings Settings) (App, error) {
	if err := settings.Validate(); err != nil {
		return App{}, ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()})
	}
	app, err := s.store.GetByID(ctx, nil, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return App{}, ErrAppNotFound
		}
		return App{}, err
	}
	app.UpdatedAt = db.Now()
	if err := s.store.UpdateSettings(ctx, nil, id, settings, app.UpdatedAt); err != nil {
		return App{}, err
	}
	app.Settings = settings
	s.Invalidate(id)
	return app, nil
}

// List returns apps for admin listing.
func (s *Service) List(ctx context.Context, query string, limit int) ([]App, error) {
	return s.store.List(ctx, nil, query, limit)
}

// ListActiveOrigins returns the union of allowed_origins across active apps.
func (s *Service) ListActiveOrigins(ctx context.Context) ([]string, error) {
	apps, err := s.store.ListActive(ctx, nil)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, a := range apps {
		for _, o := range a.Settings.AllowedOrigins {
			if _, ok := seen[o]; ok {
				continue
			}
			seen[o] = struct{}{}
			out = append(out, o)
		}
	}
	return out, nil
}

// CreateAPIKey mints a new key; raw is returned only once.
func (s *Service) CreateAPIKey(ctx context.Context, appID uuid.UUID, name string, scopes []string) (APIKey, string, error) {
	if _, err := s.Get(ctx, appID); err != nil {
		return APIKey{}, "", err
	}
	if name == "" {
		return APIKey{}, "", ErrInvalidSettings.WithDetails(map[string]any{"reason": "name required"})
	}
	if len(scopes) == 0 {
		scopes = append([]string{}, DefaultScopes...)
	}
	raw, hash, prefix, err := secrets.NewAPIKey()
	if err != nil {
		return APIKey{}, "", err
	}
	scopeJSON, _ := json.Marshal(scopes)
	k := APIKey{
		ID:        uuid.Must(uuid.NewV7()),
		AppID:     appID,
		Name:      name,
		Prefix:    prefix,
		KeyHash:   hash,
		Scopes:    scopeJSON,
		CreatedAt: db.Now(),
	}
	if err := s.store.InsertAPIKey(ctx, nil, k); err != nil {
		return APIKey{}, "", err
	}
	return k, raw, nil
}

// ListAPIKeys lists keys for an app (no raw secret).
func (s *Service) ListAPIKeys(ctx context.Context, appID uuid.UUID) ([]APIKey, error) {
	if _, err := s.Get(ctx, appID); err != nil {
		return nil, err
	}
	return s.store.ListAPIKeys(ctx, nil, appID)
}

// RevokeAPIKey revokes a key.
func (s *Service) RevokeAPIKey(ctx context.Context, appID, keyID uuid.UUID) error {
	if err := s.store.RevokeAPIKey(ctx, nil, appID, keyID, db.Now()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrAPIKeyNotFound
		}
		return err
	}
	return nil
}

// LookupAPIKey resolves a bearer API key string to a key row.
func (s *Service) LookupAPIKey(ctx context.Context, raw string) (APIKey, error) {
	hash := secrets.HashSHA256([]byte(raw))
	k, err := s.store.GetAPIKeyByHash(ctx, nil, hash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return APIKey{}, ErrAPIKeyNotFound
		}
		return APIKey{}, err
	}
	if k.RevokedAt.Valid {
		return APIKey{}, ErrAPIKeyRevoked
	}
	now := time.Now().UTC()
	if !k.LastUsedAt.Valid || now.Sub(k.LastUsedAt.Time.Time()) >= time.Minute {
		_ = s.store.TouchAPIKeyLastUsed(ctx, nil, k.AppID, k.ID, db.Time(now))
	}
	return k, nil
}
