package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// Service manages connections and bindings.
type Service struct {
	store     *Store
	box       *secrets.Box
	env       *EnvDefaults
	resolver  *Resolver
	publicURL string
	db        *db.DB
}

// NewService constructs an integration Service.
func NewService(database *db.DB, store *Store, box *secrets.Box, env *EnvDefaults, publicURL string, resolver *Resolver) *Service {
	return &Service{
		store:     store,
		box:       box,
		env:       env,
		resolver:  resolver,
		publicURL: strings.TrimRight(publicURL, "/"),
		db:        database,
	}
}

// Env returns env-default connections.
func (s *Service) Env() *EnvDefaults { return s.env }

// CreateConnection seals secrets and inserts a connection.
func (s *Service) CreateConnection(ctx context.Context, provider, name string, settings, secretsJSON json.RawMessage) (PublicConnection, error) {
	if !IsValidProvider(provider) {
		return PublicConnection{}, ErrInvalidProvider
	}
	if name == "" {
		return PublicConnection{}, ErrValidationFailed.WithDetails(map[string]any{"reason": "name required"})
	}
	if err := ValidateSecretsJSON(provider, secretsJSON); err != nil {
		return PublicConnection{}, ErrInvalidSecrets.WithDetails(map[string]any{"reason": err.Error()})
	}
	if settings == nil {
		settings = json.RawMessage(`{}`)
	}
	id := uuid.Must(uuid.NewV7()).String()
	enc, ver, err := s.box.Seal([]byte(secretsJSON), secrets.ConnectionAAD(id, provider))
	if err != nil {
		return PublicConnection{}, err
	}
	now := db.Now()
	c := Connection{
		ID:         id,
		Provider:   provider,
		Name:       name,
		Settings:   []byte(settings),
		SecretsEnc: enc,
		KeyVersion: ver,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.store.InsertConnection(ctx, nil, c); err != nil {
		return PublicConnection{}, err
	}
	s.resolver.Invalidate()
	return s.toPublic(ctx, c, secretsJSON)
}

// UpdateConnection patches name/settings/secrets (nil secretsJSON keeps existing).
func (s *Service) UpdateConnection(ctx context.Context, id string, name *string, settings, secretsJSON json.RawMessage) (PublicConnection, error) {
	c, err := s.store.GetConnection(ctx, nil, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return PublicConnection{}, ErrConnectionNotFound
		}
		return PublicConnection{}, err
	}
	plain, err := s.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
	if err != nil {
		return PublicConnection{}, err
	}
	if name != nil {
		c.Name = *name
	}
	if settings != nil {
		c.Settings = []byte(settings)
	}
	if secretsJSON != nil {
		if err := ValidateSecretsJSON(c.Provider, secretsJSON); err != nil {
			return PublicConnection{}, ErrInvalidSecrets.WithDetails(map[string]any{"reason": err.Error()})
		}
		enc, ver, err := s.box.Seal([]byte(secretsJSON), secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return PublicConnection{}, err
		}
		c.SecretsEnc = enc
		c.KeyVersion = ver
		plain = []byte(secretsJSON)
	}
	c.UpdatedAt = db.Now()
	if err := s.store.UpdateConnection(ctx, nil, c); err != nil {
		return PublicConnection{}, err
	}
	s.resolver.Invalidate()
	return s.toPublic(ctx, c, plain)
}

// DeleteConnection removes a connection if unused.
func (s *Service) DeleteConnection(ctx context.Context, id string) error {
	n, err := s.store.CountBindings(ctx, nil, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrConnectionInUse
	}
	if err := s.store.DeleteConnection(ctx, nil, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrConnectionNotFound
		}
		return err
	}
	s.resolver.Invalidate()
	return nil
}

// GetConnectionPublic returns a masked connection.
func (s *Service) GetConnectionPublic(ctx context.Context, id string) (PublicConnection, error) {
	c, err := s.store.GetConnection(ctx, nil, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return PublicConnection{}, ErrConnectionNotFound
		}
		return PublicConnection{}, err
	}
	plain, err := s.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
	if err != nil {
		return PublicConnection{}, err
	}
	return s.toPublic(ctx, c, plain)
}

// ListConnectionsPublic lists DB connections plus env defaults, all masked.
func (s *Service) ListConnectionsPublic(ctx context.Context, provider string) ([]PublicConnection, error) {
	rows, err := s.store.ListConnections(ctx, nil, provider)
	if err != nil {
		return nil, err
	}
	out := make([]PublicConnection, 0, len(rows)+4)
	for _, c := range rows {
		plain, err := s.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return nil, err
		}
		pc, err := s.toPublic(ctx, c, plain)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	for _, c := range s.env.List() {
		if provider != "" && c.Provider != provider {
			continue
		}
		_, sec, _ := s.env.Get(c.Provider)
		pc, err := s.toPublic(ctx, c, sec)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, nil
}

// BindingInput is one provider binding in a config replace.
type BindingInput struct {
	ConnectionID string          `json:"connection_id"` // UUID or "default"
	Settings     json.RawMessage `json:"settings"`
}

// ReplaceBindingsForApp replaces all bindings for an app atomically.
func (s *Service) ReplaceBindingsForApp(ctx context.Context, appID uuid.UUID, inputs map[string]BindingInput) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := s.store.DeleteBindingsForApp(ctx, tx, appID); err != nil {
			return err
		}
		now := db.Now()
		for provider, in := range inputs {
			if !IsValidProvider(provider) {
				return ErrInvalidProvider
			}
			var connID *string
			switch in.ConnectionID {
			case "", DefaultConnectionID:
				if !s.env.Has(provider) {
					return ErrNotConfigured.WithDetails(map[string]any{"provider": provider})
				}
				connID = nil // explicit env default
			default:
				c, err := s.store.GetConnection(ctx, tx, in.ConnectionID)
				if err != nil {
					if errors.Is(err, db.ErrNotFound) {
						return ErrConnectionNotFound
					}
					return err
				}
				if c.Provider != provider {
					return ErrProviderMismatch
				}
				connID = &in.ConnectionID
			}

			if IsExclusive(provider) {
				existing, err := s.store.FindExclusiveBinding(ctx, tx, provider, connID)
				if err != nil && !errors.Is(err, db.ErrNotFound) {
					return err
				}
				if err == nil && existing.AppID != appID {
					return ErrConnectionAlreadyBound.WithDetails(map[string]any{"app_id": existing.AppID.String()})
				}
			}

			settings := []byte(in.Settings)
			if len(settings) == 0 {
				settings = []byte(`{}`)
			}
			b := Binding{
				AppID:        appID,
				Provider:     provider,
				ConnectionID: connID,
				Settings:     settings,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := s.store.UpsertBinding(ctx, tx, b); err != nil {
				var u db.ErrUniqueViolation
				if errors.As(err, &u) {
					return ErrConnectionAlreadyBound
				}
				return err
			}
		}
		return nil
	})
}

// ListBindings returns bindings for an app.
func (s *Service) ListBindings(ctx context.Context, appID uuid.UUID) ([]Binding, error) {
	return s.store.ListBindingsForApp(ctx, nil, appID)
}

func (s *Service) toPublic(ctx context.Context, c Connection, plain []byte) (PublicConnection, error) {
	masked, err := MaskSecretsJSON(c.Provider, plain)
	if err != nil {
		return PublicConnection{}, err
	}
	var used []uuid.UUID
	if !c.ReadOnly && c.ID != DefaultConnectionID {
		bindings, err := s.store.ListBindingsForConnection(ctx, nil, c.ID)
		if err != nil {
			return PublicConnection{}, err
		}
		for _, b := range bindings {
			used = append(used, b.AppID)
		}
	}
	pc := PublicConnection{
		ID:         c.ID,
		Provider:   c.Provider,
		Name:       c.Name,
		Settings:   json.RawMessage(c.Settings),
		Secrets:    masked,
		UsedByApps: used,
		ReadOnly:   c.ReadOnly,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
	if c.Provider == ProviderStripe && s.publicURL != "" {
		pc.WebhookURL = fmt.Sprintf("%s/api/v1/webhooks/stripe/%s", s.publicURL, c.ID)
	}
	if c.Provider == ProviderAppleIAP && s.publicURL != "" {
		pc.WebhookURL = fmt.Sprintf("%s/api/v1/webhooks/apple/%s", s.publicURL, c.ID)
	}
	pc.RedirectURL = OAuthRedirectURL(s.publicURL, c.Provider)
	return pc, nil
}

// ProviderRedirectURL is the OAuth callback for provider, or empty when it does not apply.
func (s *Service) ProviderRedirectURL(provider string) string {
	return OAuthRedirectURL(s.publicURL, provider)
}

// OAuthRedirectURL is the provider callback registered in Google, GitHub, or Apple.
// It is identical for every app and connection of that provider.
func OAuthRedirectURL(publicURL, provider string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	var name string
	switch provider {
	case ProviderGoogleOAuth:
		name = "google"
	case ProviderGitHubOAuth:
		name = "github"
	case ProviderAppleSignIn:
		name = "apple"
	default:
		return ""
	}
	return publicURL + "/api/v1/auth/oauth/" + name + "/callback"
}
