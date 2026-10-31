package integration

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// Resolver resolves per-app provider credentials with caching.
type Resolver struct {
	store *Store
	box   *secrets.Box
	env   *EnvDefaults

	mu    sync.RWMutex
	cache map[cacheKey]cacheVal
	ttl   time.Duration
}

type cacheKey struct {
	appID    uuid.UUID
	provider string
}

type cacheVal struct {
	r  Resolved
	at time.Time
}

// NewResolver constructs a Resolver.
func NewResolver(store *Store, box *secrets.Box, env *EnvDefaults) *Resolver {
	return &Resolver{
		store: store,
		box:   box,
		env:   env,
		cache: map[cacheKey]cacheVal{},
		ttl:   60 * time.Second,
	}
}

// Invalidate clears cached resolutions (call after admin writes on this instance).
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	r.cache = map[cacheKey]cacheVal{}
	r.mu.Unlock()
}

// For resolves credentials for an app and provider.
func (r *Resolver) For(ctx context.Context, appID uuid.UUID, provider string) (Resolved, error) {
	if !IsValidProvider(provider) {
		return Resolved{}, ErrInvalidProvider
	}
	key := cacheKey{appID: appID, provider: provider}
	r.mu.RLock()
	if v, ok := r.cache[key]; ok && time.Since(v.at) < r.ttl {
		r.mu.RUnlock()
		return v.r, nil
	}
	r.mu.RUnlock()

	resolved, err := r.resolve(ctx, appID, provider)
	if err != nil {
		return Resolved{}, err
	}
	r.mu.Lock()
	r.cache[key] = cacheVal{r: resolved, at: time.Now()}
	r.mu.Unlock()
	return resolved, nil
}

func (r *Resolver) resolve(ctx context.Context, appID uuid.UUID, provider string) (Resolved, error) {
	b, err := r.store.GetBinding(ctx, nil, appID, provider)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return Resolved{}, err
	}
	if err == nil {
		// Explicit binding.
		if b.ConnectionID == nil {
			// Bound to env default.
			c, sec, ok := r.env.Get(provider)
			if !ok {
				return Resolved{}, ErrNotConfigured
			}
			return Resolved{
				ConnectionID:    DefaultConnectionID,
				Provider:        provider,
				Source:          "binding",
				Settings:        c.Settings,
				Secrets:         sec,
				BindingSettings: b.Settings,
			}, nil
		}
		c, err := r.store.GetConnection(ctx, nil, *b.ConnectionID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return Resolved{}, ErrNotConfigured
			}
			return Resolved{}, err
		}
		if c.Provider != provider {
			return Resolved{}, ErrProviderMismatch
		}
		plain, err := r.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{
			ConnectionID:    c.ID,
			Provider:        provider,
			Source:          "binding",
			Settings:        c.Settings,
			Secrets:         plain,
			BindingSettings: b.Settings,
		}, nil
	}

	// No binding: shared providers may fall back to env default.
	if IsExclusive(provider) {
		return Resolved{}, ErrNotConfigured
	}
	c, sec, ok := r.env.Get(provider)
	if !ok {
		return Resolved{}, ErrNotConfigured
	}
	return Resolved{
		ConnectionID: DefaultConnectionID,
		Provider:     provider,
		Source:       "default",
		Settings:     c.Settings,
		Secrets:      sec,
	}, nil
}

// OpenConnectionDirect decrypts a connection by ID without checking app bindings.
func (r *Resolver) OpenConnectionDirect(ctx context.Context, id, provider string) (Resolved, error) {
	if id == "" || id == DefaultConnectionID {
		c, sec, ok := r.env.Get(provider)
		if !ok {
			return Resolved{}, ErrNotConfigured
		}
		return Resolved{
			ConnectionID: DefaultConnectionID,
			Provider:     provider,
			Source:       "default",
			Settings:     c.Settings,
			Secrets:      sec,
		}, nil
	}
	c, err := r.store.GetConnection(ctx, nil, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Resolved{}, ErrConnectionNotFound
		}
		return Resolved{}, err
	}
	if provider != "" && c.Provider != provider {
		return Resolved{}, ErrProviderMismatch
	}
	plain, err := r.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		ConnectionID: c.ID,
		Provider:     c.Provider,
		Source:       "connection",
		Settings:     c.Settings,
		Secrets:      plain,
	}, nil
}

// AppForConnection returns the single app bound to an exclusive connection.
func (r *Resolver) AppForConnection(ctx context.Context, provider, connectionID string) (uuid.UUID, error) {
	if !IsExclusive(provider) {
		return uuid.Nil, ErrInvalidProvider
	}
	var connID *string
	if connectionID == DefaultConnectionID {
		connID = nil
	} else {
		connID = &connectionID
	}
	b, err := r.store.FindExclusiveBinding(ctx, nil, provider, connID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return uuid.Nil, ErrNotConfigured
		}
		return uuid.Nil, err
	}
	return b.AppID, nil
}

// EffectiveSource describes how a provider is configured for an app (for admin config responses).
func (r *Resolver) EffectiveSource(ctx context.Context, appID uuid.UUID, provider string) (connectionID, source string) {
	resolved, err := r.For(ctx, appID, provider)
	if err != nil {
		return "", "none"
	}
	return resolved.ConnectionID, resolved.Source
}
