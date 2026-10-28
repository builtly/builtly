package auth

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/builtly/builtly/internal/auth/provider"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/logx"
)

// AppleRevokePayload is published on user.deleted for the apple revoke subscriber.
type AppleRevokePayload struct {
	UserID             string   `json:"user_id"`
	AppleRefreshTokens []string `json:"apple_refresh_tokens,omitempty"`
}

// AppleRevokeSubscriber revokes Apple refresh tokens after account deletion.
type AppleRevokeSubscriber struct {
	providers *provider.Registry
	resolver  *integration.Resolver
	log       *slog.Logger
}

// NewAppleRevokeSubscriber constructs the subscriber.
func NewAppleRevokeSubscriber(reg *provider.Registry, resolver *integration.Resolver, log *slog.Logger) *AppleRevokeSubscriber {
	if log == nil {
		log = slog.Default()
	}
	return &AppleRevokeSubscriber{providers: reg, resolver: resolver, log: log}
}

// Handle implements outbox.Handler for auth.apple_revoke.
func (s *AppleRevokeSubscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	var p AppleRevokePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	if len(p.AppleRefreshTokens) == 0 {
		return nil
	}
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: errMissingApp}
	}
	resolved, err := s.resolver.For(ctx, *row.AppID, integration.ProviderAppleSignIn)
	if err == integration.ErrNotConfigured {
		return outbox.ErrNotConfigured
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	cfg, err := provider.ParseConfig(integration.ProviderAppleSignIn, resolved.Settings, resolved.Secrets)
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	log := logx.From(ctx)
	for _, tok := range p.AppleRefreshTokens {
		if err := s.providers.AppleRevoke(ctx, cfg, tok); err != nil {
			log.Warn("apple revoke failed", "err", err)
			return outbox.ErrRetryable{Err: err}
		}
	}
	return nil
}

var errMissingApp = &missingAppError{}

type missingAppError struct{}

func (e *missingAppError) Error() string { return "apple_revoke: missing app_id" }

// CollectAppleRefreshTokens decrypts Apple refresh tokens from identity rows.
func CollectAppleRefreshTokens(idents []Identity, open func(enc, aad []byte) ([]byte, error)) []string {
	var out []string
	for _, id := range idents {
		if id.Provider != provider.Apple || len(id.ProviderRefreshEnc) == 0 {
			continue
		}
		plain, err := open(id.ProviderRefreshEnc, []byte(id.ID.String()))
		if err != nil {
			continue
		}
		if s := string(plain); s != "" {
			out = append(out, s)
		}
	}
	return out
}
