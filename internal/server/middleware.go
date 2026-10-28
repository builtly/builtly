package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/logx"
)

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)
		ctx := logx.With(r.Context(), logx.From(r.Context()).With("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func realIP(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = httpx.ClientIP(r, cfg.TrustedProxies) // computed for access log via middleware below
			next.ServeHTTP(w, r)
		})
	}
}

func accessLog(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = r.URL.Path
			}
			logx.From(r.Context()).Info("access",
				"method", r.Method,
				"route", route,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", ww.BytesWritten(),
				"remote_ip", httpx.ClientIP(r, cfg.TrustedProxies),
			)
		})
	}
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.From(r.Context()).Error("panic", "recover", rec)
				httpx.WriteError(w, r, apperr.Internal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func secHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func bodyLimit(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}
