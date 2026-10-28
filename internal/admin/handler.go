package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/tenant"
)

// Handler serves /api/v1/admin/* endpoints.
type Handler struct {
	db        *db.DB
	tenant    *tenant.Service
	integ     *integration.Service
	resolv    *integration.Resolver
	audit     *AuditStore
	tStore    *tenant.Store
	iStore    *integration.Store
	tMW       *tenant.Middleware
	users     *auth.Store
	credits   *credit.Service
	purchases *billing.Store
	outbox    *outbox.Store
	box       *secrets.Box
	catalog   *catalog.Service
	stripe    *stripeapi.Client
	referrals *referral.Service
}

// NewHandler constructs an admin Handler.
func NewHandler(
	database *db.DB,
	tSvc *tenant.Service,
	iSvc *integration.Service,
	resolver *integration.Resolver,
	audit *AuditStore,
	tStore *tenant.Store,
	iStore *integration.Store,
	tMW *tenant.Middleware,
	users *auth.Store,
	credits *credit.Service,
	purchases *billing.Store,
	outboxStore *outbox.Store,
	box *secrets.Box,
	cat *catalog.Service,
	stripe *stripeapi.Client,
	referrals *referral.Service,
) *Handler {
	return &Handler{
		db: database, tenant: tSvc, integ: iSvc, resolv: resolver,
		audit: audit, tStore: tStore, iStore: iStore, tMW: tMW,
		users: users, credits: credits, purchases: purchases,
		outbox: outboxStore, box: box,
		catalog: cat, stripe: stripe, referrals: referrals,
	}
}

// Routes mounts admin routes on r (already behind Auth).
func (h *Handler) Routes(r chi.Router) {
	r.Get("/apps", h.listApps)
	r.Post("/apps", h.createApp)
	r.Get("/apps/{app_id}", h.getApp)
	r.Patch("/apps/{app_id}", h.patchApp)
	r.Get("/apps/{app_id}/config", h.getConfig)
	r.Put("/apps/{app_id}/config", h.putConfig)
	r.Get("/apps/{app_id}/api-keys", h.listAPIKeys)
	r.Post("/apps/{app_id}/api-keys", h.createAPIKey)
	r.Delete("/apps/{app_id}/api-keys/{key_id}", h.revokeAPIKey)

	r.Get("/apps/{app_id}/products", h.listProducts)
	r.Post("/apps/{app_id}/products", h.createProduct)
	r.Patch("/apps/{app_id}/products/{product_id}", h.patchProduct)

	r.Get("/apps/{app_id}/users", h.listUsers)
	r.Get("/apps/{app_id}/users/{user_id}", h.getUser)
	r.Patch("/apps/{app_id}/users/{user_id}", h.patchUser)
	r.Get("/apps/{app_id}/users/{user_id}/ledger", h.userLedger)
	r.Post("/apps/{app_id}/users/{user_id}/credits", h.adjustCredits)
	r.Post("/apps/{app_id}/users/{user_id}/credits/clear-debt", h.clearDebt)

	r.Get("/apps/{app_id}/referrals", h.listReferrals)
	r.Post("/apps/{app_id}/referrals/{referral_id}/void", h.voidReferral)

	r.Get("/apps/{app_id}/purchases", h.listAppPurchases)
	r.Get("/apps/{app_id}/ledger", h.listAppLedger)

	r.Get("/connections", h.listConnections)
	r.Post("/connections", h.createConnection)
	r.Get("/connections/{connection_id}", h.getConnection)
	r.Patch("/connections/{connection_id}", h.patchConnection)
	r.Delete("/connections/{connection_id}", h.deleteConnection)

	r.Get("/outbox", h.listOutbox)
	r.Post("/outbox/{id}/retry", h.retryOutbox)

	r.Get("/audit-log", h.listAudit)
}

func (h *Handler) listApps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	apps, err := h.tenant.List(r.Context(), q, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		items = append(items, map[string]any{
			"id": a.ID, "slug": a.Slug, "name": a.Name, "status": a.Status,
			"user_count": 0, "created_at": a.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug     string          `json:"slug"`
		Name     string          `json:"name"`
		Settings tenant.Settings `json:"settings"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	app, err := h.tenant.Create(r.Context(), body.Slug, body.Name, body.Settings)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.tMW.InvalidateCORS()
	_ = h.writeAudit(r, nil, "app.create", "app", app.ID.String(), map[string]any{
		"slug": app.Slug, "name": app.Name,
	})
	httpx.WriteJSON(w, http.StatusCreated, app)
}

func (h *Handler) getApp(w http.ResponseWriter, r *http.Request) {
	id, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	app, err := h.tenant.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, app)
}

func (h *Handler) patchApp(w http.ResponseWriter, r *http.Request) {
	id, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Name   *string `json:"name"`
		Status *string `json:"status"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	app, err := h.tenant.UpdateMeta(r.Context(), id, body.Name, body.Status)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.tMW.InvalidateCORS()
	_ = h.writeAudit(r, &id, "app.update", "app", id.String(), map[string]any{
		"name": body.Name, "status": body.Status,
	})
	httpx.WriteJSON(w, http.StatusOK, app)
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	id, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cfg, err := h.loadConfig(r, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg)
}

func (h *Handler) putConfig(w http.ResponseWriter, r *http.Request) {
	id, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Settings     tenant.Settings                     `json:"settings"`
		Integrations map[string]integration.BindingInput `json:"integrations"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if body.Integrations == nil {
		body.Integrations = map[string]integration.BindingInput{}
	}
	settings, err := tenant.MergeSettings(tenant.DefaultSettings(), body.Settings)
	if err != nil {
		httpx.WriteError(w, r, tenant.ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()}))
		return
	}
	if err := settings.Validate(); err != nil {
		httpx.WriteError(w, r, tenant.ErrInvalidSettings.WithDetails(map[string]any{"reason": err.Error()}))
		return
	}
	body.Settings = settings

	err = h.db.WithTx(r.Context(), func(tx *sqlx.Tx) error {
		app, err := h.tStore.GetByID(r.Context(), tx, id)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return tenant.ErrAppNotFound
			}
			return err
		}
		now := db.Now()
		if err := h.tStore.UpdateSettings(r.Context(), tx, id, body.Settings, now); err != nil {
			return err
		}
		_ = app
		// Replace bindings inside this tx.
		if err := h.iStore.DeleteBindingsForApp(r.Context(), tx, id); err != nil {
			return err
		}
		for provider, in := range body.Integrations {
			if err := h.upsertBindingTx(r, tx, id, provider, in, now); err != nil {
				return err
			}
		}
		return h.writeAuditTx(r, tx, &id, "app.config.update", "app", id.String(), map[string]any{
			"settings": body.Settings, "integrations": body.Integrations,
		})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.tenant.Invalidate(id)
	h.tMW.InvalidateCORS()
	h.resolv.Invalidate()
	cfg, err := h.loadConfig(r, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg)
}

func (h *Handler) upsertBindingTx(r *http.Request, tx *sqlx.Tx, appID uuid.UUID, provider string, in integration.BindingInput, now db.Time) error {
	if !integration.IsValidProvider(provider) {
		return integration.ErrInvalidProvider
	}
	var connID *string
	switch in.ConnectionID {
	case "", integration.DefaultConnectionID:
		if !h.integ.Env().Has(provider) {
			return integration.ErrNotConfigured.WithDetails(map[string]any{"provider": provider})
		}
		connID = nil
	default:
		c, err := h.iStore.GetConnection(r.Context(), tx, in.ConnectionID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return integration.ErrConnectionNotFound
			}
			return err
		}
		if c.Provider != provider {
			return integration.ErrProviderMismatch
		}
		connID = &in.ConnectionID
	}
	if integration.IsExclusive(provider) {
		existing, err := h.iStore.FindExclusiveBinding(r.Context(), tx, provider, connID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
		if err == nil && existing.AppID != appID {
			return integration.ErrConnectionAlreadyBound.WithDetails(map[string]any{"app_id": existing.AppID.String()})
		}
	}
	settings := []byte(in.Settings)
	if len(settings) == 0 {
		settings = []byte(`{}`)
	}
	return h.iStore.UpsertBinding(r.Context(), tx, integration.Binding{
		AppID: appID, Provider: provider, ConnectionID: connID,
		Settings: settings, CreatedAt: now, UpdatedAt: now,
	})
}

func (h *Handler) loadConfig(r *http.Request, appID uuid.UUID) (map[string]any, error) {
	app, err := h.tenant.Get(r.Context(), appID)
	if err != nil {
		return nil, err
	}
	bindings, err := h.integ.ListBindings(r.Context(), appID)
	if err != nil {
		return nil, err
	}
	integOut := map[string]any{}
	bound := map[string]integration.Binding{}
	for _, b := range bindings {
		bound[b.Provider] = b
		cid := integration.DefaultConnectionID
		if b.ConnectionID != nil {
			cid = *b.ConnectionID
		}
		entry := map[string]any{"connection_id": cid, "source": "binding"}
		if len(b.Settings) > 0 && string(b.Settings) != "{}" {
			entry["settings"] = json.RawMessage(b.Settings)
		}
		integOut[b.Provider] = entry
	}
	providers := []string{
		integration.ProviderStripe, integration.ProviderResend, integration.ProviderR2,
		integration.ProviderGoogleOAuth, integration.ProviderGitHubOAuth, integration.ProviderAppleSignIn,
		integration.ProviderAppleIAP, integration.ProviderGooglePlay, integration.ProviderPostHog,
		integration.ProviderPromoteKit, integration.ProviderAffonso,
	}
	for _, p := range providers {
		if _, ok := bound[p]; ok {
			continue
		}
		_, source := h.resolv.EffectiveSource(r.Context(), appID, p)
		entry := map[string]any{"source": source}
		if source == "default" {
			entry["connection_id"] = integration.DefaultConnectionID
		}
		integOut[p] = entry
	}
	return map[string]any{
		"settings":     app.Settings,
		"integrations": integOut,
	}, nil
}

func (h *Handler) listConnections(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	items, err := h.integ.ListConnectionsPublic(r.Context(), provider)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string          `json:"provider"`
		Name     string          `json:"name"`
		Settings json.RawMessage `json:"settings"`
		Secrets  json.RawMessage `json:"secrets"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	settings := body.Settings
	if body.Provider == integration.ProviderStripe {
		enriched, err := h.enrichStripeSettings(r, settings, body.Secrets, "")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		settings = enriched
	}
	pc, err := h.integ.CreateConnection(r.Context(), body.Provider, body.Name, settings, body.Secrets)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, nil, "connection.create", "connection", pc.ID, map[string]any{
		"provider": pc.Provider, "name": pc.Name, "secrets": body.Secrets,
	})
	httpx.WriteJSON(w, http.StatusCreated, pc)
}

func (h *Handler) getConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connection_id")
	pc, err := h.integ.GetConnectionPublic(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pc)
}

func (h *Handler) patchConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connection_id")
	var body struct {
		Name     *string         `json:"name"`
		Settings json.RawMessage `json:"settings"`
		Secrets  json.RawMessage `json:"secrets"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	settings := body.Settings
	existing, err := h.integ.GetConnectionPublic(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if existing.Provider == integration.ProviderStripe && (body.Secrets != nil || body.Settings != nil) {
		sec := body.Secrets
		if sec == nil {
			// keep existing secrets — open via store path in enrich with empty secrets uses existing key from update flow
			sec = json.RawMessage(`{}`)
		}
		// When secrets omitted, UpdateConnection keeps them; still re-validate if settings/secrets change with stripe client.
		if body.Secrets != nil {
			enriched, err := h.enrichStripeSettings(r, coalesceSettings(settings, existing.Settings), body.Secrets, id)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			settings = enriched
		} else if body.Settings != nil {
			// settings-only patch: preserve account_id from existing
			settings = body.Settings
		}
		_ = sec
	}
	pc, err := h.integ.UpdateConnection(r.Context(), id, body.Name, settings, body.Secrets)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, nil, "connection.update", "connection", id, map[string]any{
		"name": body.Name, "secrets": body.Secrets,
	})
	httpx.WriteJSON(w, http.StatusOK, pc)
}

func coalesceSettings(patch, existing json.RawMessage) json.RawMessage {
	if len(patch) > 0 {
		return patch
	}
	return existing
}

func (h *Handler) enrichStripeSettings(r *http.Request, settings, secretsJSON json.RawMessage, excludeID string) (json.RawMessage, error) {
	var sec integration.StripeSecrets
	if err := json.Unmarshal(secretsJSON, &sec); err != nil || sec.SecretKey == "" {
		return nil, integration.ErrInvalidSecrets
	}
	if h.stripe == nil {
		return nil, integration.ErrValidationFailed.WithDetails(map[string]any{"reason": "stripe client not configured"})
	}
	acc, err := h.stripe.GetAccount(r.Context(), sec.SecretKey)
	if err != nil {
		return nil, integration.ErrValidationFailed.WithDetails(map[string]any{"reason": err.Error()})
	}
	merged, livemode, err := integration.MergeStripeAccountSettings(settings, acc.ID, sec.SecretKey)
	if err != nil {
		return nil, err
	}
	dup, err := h.integ.FindStripeAccountDuplicate(r.Context(), acc.ID, livemode, excludeID)
	if err != nil {
		return nil, err
	}
	if dup != "" {
		return nil, integration.ErrConnectionDuplicate.WithDetails(map[string]any{"connection_id": dup})
	}
	return merged, nil
}

func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "connection_id")
	if err := h.integ.DeleteConnection(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, nil, "connection.delete", "connection", id, map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	keys, err := h.tenant.ListAPIKeys(r.Context(), appID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		items = append(items, map[string]any{
			"id": k.ID, "name": k.Name, "prefix": k.Prefix,
			"scopes": json.RawMessage(k.Scopes), "revoked_at": nullTime(k.RevokedAt),
			"last_used_at": nullTime(k.LastUsedAt), "created_at": k.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	k, raw, err := h.tenant.CreateAPIKey(r.Context(), appID, body.Name, body.Scopes)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "api_key.create", "api_key", k.ID.String(), map[string]any{
		"name": k.Name, "key": raw,
	})
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "prefix": k.Prefix,
		"scopes": json.RawMessage(k.Scopes), "key": raw, "created_at": k.CreatedAt,
	})
}

func (h *Handler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	appID, err := parseAppID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	keyID, err := uuid.Parse(chi.URLParam(r, "key_id"))
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid key_id"}))
		return
	}
	if err := h.tenant.RevokeAPIKey(r.Context(), appID, keyID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.writeAudit(r, &appID, "api_key.revoke", "api_key", keyID.String(), map[string]any{})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	var appID *uuid.UUID
	if s := r.URL.Query().Get("app_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.WriteError(w, r, apperr.BadRequest)
			return
		}
		appID = &id
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.audit.List(r.Context(), appID, r.URL.Query().Get("action"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (h *Handler) writeAudit(r *http.Request, appID *uuid.UUID, action, targetType, targetID string, diff map[string]any) error {
	return h.writeAuditTx(r, nil, appID, action, targetType, targetID, diff)
}

func (h *Handler) writeAuditTx(r *http.Request, q db.Querier, appID *uuid.UUID, action, targetType, targetID string, diff map[string]any) error {
	reqID := r.Header.Get("X-Request-ID")
	if reqID == "" {
		reqID = "-"
	}
	return h.audit.Insert(r.Context(), q, AuditEntry{
		ID: uuid.Must(uuid.NewV7()), Actor: "admin", Action: action,
		AppID: appID, TargetType: targetType, TargetID: targetID,
		Diff: MustJSON(RedactDiffMap(diff)), IP: httpx.ClientIP(r, nil),
		RequestID: reqID, CreatedAt: db.Now(),
	})
}

func parseAppID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		return uuid.Nil, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid app_id"})
	}
	return id, nil
}

func nullTime(n db.NullTime) any {
	if !n.Valid {
		return nil
	}
	return n.Time
}
