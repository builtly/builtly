// Package server is the composition root: wiring, router, HTTP server, workers.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/builtly/builtly/internal/admin"
	"github.com/builtly/builtly/internal/ai"
	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/email"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/secrets"
	"github.com/builtly/builtly/internal/referral"
	"github.com/builtly/builtly/internal/storage"
	"github.com/builtly/builtly/internal/tenant"
	"github.com/builtly/builtly/internal/version"
	"github.com/builtly/builtly/web"
)

// Deps are process-scoped services wired once at startup.
type Deps struct {
	Tenant           *tenant.Service
	TenantMW         *tenant.Middleware
	TenantStore      *tenant.Store
	Integration      *integration.Service
	IntegStore       *integration.Store
	Resolver         *integration.Resolver
	Secrets          *secrets.Box
	AdminAuth        *admin.Auth
	Admin            *admin.Handler
	AuthMW           *auth.Middleware
	Credits          *credit.Service
	CreditUser       *credit.UserHandler
	AI               *ai.Handler
	OutboxDispatcher *outbox.Dispatcher
	Auth             *auth.Handler
	Catalog          *catalog.Handler
	Billing          *billing.Handler
	VoidedPoller     *billing.VoidedPoller
	Storage          *storage.Handler
	StorageCleaner   *storage.Cleaner
	Referral         *referral.Handler
	Email            *email.Handler
}

// Server owns the HTTP listener and shared dependencies.
type Server struct {
	cfg    *config.Config
	db     *db.DB
	log    *slog.Logger
	deps   Deps
	http   *http.Server
	router chi.Router
}

// New constructs a Server (does not listen).
func New(cfg *config.Config, database *db.DB, log *slog.Logger, deps Deps) *Server {
	s := &Server{cfg: cfg, db: database, log: log, deps: deps}
	s.router = s.buildRouter()
	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	return s
}

// Router exposes the chi router for tests.
func (s *Server) Router() http.Handler { return s.router }

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(requestID)
	r.Use(realIP(s.cfg))
	r.Use(accessLog(s.cfg))
	r.Use(recoverer)
	r.Use(secHeaders)

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/api/v1/setup/status", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"needs_setup":    false,
			"requires_token": false,
		})
	})

	if s.cfg.AdminEnabled {
		adminFS, err := fs.Sub(web.Admin, "admin")
		if err != nil {
			panic("web.Admin embed: " + err.Error())
		}
		r.With(adminUIHeaders).Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
		})
		r.With(adminUIHeaders).Handle("/admin/*", http.StripPrefix("/admin", spaFileServer(adminFS)))
	}

	// Provider webhooks: up to 1 MiB, no tenant middleware.
	if s.deps.Billing != nil {
		r.Group(func(wh chi.Router) {
			wh.Use(bodyLimit(1 << 20))
			wh.Post("/api/v1/webhooks/stripe/{connection_id}", s.deps.Billing.StripeWebhook)
			wh.Post("/api/v1/webhooks/apple/{connection_id}", s.deps.Billing.AppleWebhook)
		})
	}

	r.Group(func(apiRoot chi.Router) {
		apiRoot.Use(bodyLimit(64 << 10))

		if s.cfg.AdminEnabled && s.deps.Admin != nil && s.deps.AdminAuth != nil {
			apiRoot.Route("/api/v1/admin", func(ar chi.Router) {
				ar.Use(s.deps.AdminAuth.Middleware)
				s.deps.Admin.Routes(ar)
			})
		}

		apiRoot.Route("/api/v1", func(api chi.Router) {
			api.Use(s.deps.TenantMW.CORS)

			// OAuth browser routes: tenant from ?app_id / oauth state (not X-App-ID).
			if s.deps.Auth != nil {
				api.Get("/auth/oauth/{provider}", s.deps.Auth.OAuthStart)
				api.Get("/auth/oauth/{provider}/callback", s.deps.Auth.OAuthCallback)
				api.Post("/auth/oauth/{provider}/callback", s.deps.Auth.OAuthCallback)
			}

			api.Group(func(pub chi.Router) {
				pub.Use(s.deps.TenantMW.Tenant)
				pub.Get("/_ping", func(w http.ResponseWriter, r *http.Request) {
					app, _ := tenant.FromContext(r.Context())
					httpx.WriteJSON(w, http.StatusOK, map[string]string{
						"app_id": app.ID.String(),
						"slug":   app.Slug,
					})
				})
				if s.deps.Catalog != nil {
					pub.Get("/products", s.deps.Catalog.ListProducts)
				}

				if s.deps.Auth != nil {
					pub.Get("/auth/config", s.deps.Auth.PublicConfig)
					pub.Post("/auth/register", s.deps.Auth.Register)
					pub.Post("/auth/login", s.deps.Auth.Login)
					pub.Post("/auth/refresh", s.deps.Auth.Refresh)
					pub.Post("/auth/logout", s.deps.Auth.Logout)
					pub.Post("/auth/email/verify", s.deps.Auth.VerifyEmail)
					pub.Post("/auth/password/forgot", s.deps.Auth.ForgotPassword)
					pub.Post("/auth/password/reset", s.deps.Auth.ResetPassword)
					pub.Post("/auth/oauth/exchange", s.deps.Auth.OAuthExchange)
					pub.Post("/auth/apple", s.deps.Auth.AppleNative)
					pub.Post("/auth/google", s.deps.Auth.GoogleNative)
				}

				pub.Group(func(authed chi.Router) {
					authed.Use(s.deps.AuthMW.Authenticate)
					if s.deps.AI != nil {
						authed.Post("/ai/consume", s.deps.AI.Consume)
						authed.Post("/ai/chat/completions", s.deps.AI.ChatCompletions)
						authed.Post("/ai/presets/{slug}/run", s.deps.AI.RunPreset)
					}
					if s.deps.Auth != nil {
						authed.Post("/auth/email/verify/request", s.deps.Auth.RequestVerify)
					}
					authed.Group(func(user chi.Router) {
						user.Use(auth.RequireUser)
						user.Get("/user/credits", s.deps.CreditUser.GetBalance)
						user.Get("/user/credits/ledger", s.deps.CreditUser.GetLedger)
						if s.deps.Auth != nil {
							user.Get("/user/me", s.deps.Auth.GetMe)
							user.Patch("/user/me", s.deps.Auth.PatchMe)
							user.Delete("/user/me", s.deps.Auth.DeleteMe)
							user.Post("/user/me/avatar", s.deps.Auth.CreateAvatar)
							user.Post("/user/me/avatar/complete", s.deps.Auth.CompleteAvatar)
						}
						if s.deps.Billing != nil {
							user.Post("/checkout/stripe", s.deps.Billing.CheckoutStripe)
							user.Post("/billing/verify-iap", s.deps.Billing.VerifyIAP)
							user.Post("/billing/transfer-guest-credits", s.deps.Billing.TransferGuestCredits)
						}
						if s.deps.Storage != nil {
							user.Get("/files", s.deps.Storage.List)
							user.Post("/files", s.deps.Storage.Create)
							user.Get("/files/{file_id}", s.deps.Storage.Get)
							user.Delete("/files/{file_id}", s.deps.Storage.Delete)
							user.Post("/files/{file_id}/complete", s.deps.Storage.Complete)
						}
						if s.deps.Referral != nil {
							user.Get("/user/referral", s.deps.Referral.Get)
							user.Get("/user/referral/referees", s.deps.Referral.ListReferees)
							user.Post("/user/referral/apply", s.deps.Referral.Apply)
						}
						if s.deps.Email != nil {
							user.Post("/email/send", s.deps.Email.Send)
						}
					})
				})
			})
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]string{"code": "not_found", "message": "not found"},
		})
	})
	return r
}

// img-src allows https: so admin can render user avatars from GitHub/Google/R2 CDNs.
const adminCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

func adminUIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", adminCSP)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// spaFileServer serves static files from fsys, falling back to index.html for unknown paths.
func spaFileServer(fsys fs.FS) http.Handler {
	fileServer := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(fsys, path); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": version.Version,
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		logx.From(r.Context()).Error("readyz ping failed", "err", err)
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{"code": "not_ready", "message": "database unavailable"},
		})
		return
	}
	pending, err := s.db.Pending(ctx)
	if err != nil {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{"code": "not_ready", "message": "migration check failed"},
		})
		return
	}
	if len(pending) > 0 {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"code":    "not_ready",
				"message": "migrations pending",
			},
		})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Run listens until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	if s.cfg.WorkersEnabled && s.deps.OutboxDispatcher != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.log.Info("outbox dispatcher started")
			s.deps.OutboxDispatcher.Run(workerCtx)
			s.log.Info("outbox dispatcher stopped")
		}()
	}
	if s.cfg.WorkersEnabled && s.deps.VoidedPoller != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.log.Info("google voided poller started")
			s.deps.VoidedPoller.Run(workerCtx)
			s.log.Info("google voided poller stopped")
		}()
	}
	if s.cfg.WorkersEnabled && s.deps.StorageCleaner != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s.log.Info("storage cleaner started")
			s.deps.StorageCleaner.Run(workerCtx)
			s.log.Info("storage cleaner stopped")
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("listening", "addr", s.cfg.HTTPAddr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		s.log.Info("shutting down")
		stopWorkers()
		workers.Wait()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return <-errCh
	case err := <-errCh:
		stopWorkers()
		workers.Wait()
		return err
	}
}
