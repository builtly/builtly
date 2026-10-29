package setup

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/version"
	"github.com/builtly/builtly/web"
)

// Run listens until ctx is cancelled. Intended for NeedsSetup mode.
func Run(ctx context.Context, cfg *config.Config, log *slog.Logger, onDone func()) error {
	h := &Handler{Cfg: cfg, OnDone: onDone}
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"version": version.Version,
		})
	})
	h.Mount(r)

	if cfg.AdminEnabled {
		adminFS, err := fs.Sub(web.Admin, "admin")
		if err == nil {
			r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
			})
			r.Handle("/admin/*", http.StripPrefix("/admin", spaFileServer(adminFS)))
		}
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/#/setup", http.StatusFound)
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("setup mode listening", "addr", cfg.HTTPAddr, "data_dir", cfg.DataDir)
		log.Warn("setup token (required for wizard)", "BUILTLY_SETUP_TOKEN", cfg.SetupToken)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		err := <-errCh
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// spaFileServer matches internal/server: serve "/" as the directory index.
// Do not rewrite to "/index.html" — FileServer 301s that path to "./" and loops.
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
