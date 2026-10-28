// Package logx wraps log/slog with request-scoped context and redaction helpers.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// Setup configures the default slog logger and returns it.
func Setup(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l
}

// With returns a child context carrying logger l.
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the request-scoped logger, or slog.Default().
func From(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && v != nil {
		return v
	}
	return slog.Default()
}

// RedactEmail masks an email for info+ logs: a***@example.com
func RedactEmail(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	local, domain := email[:at], email[at+1:]
	if local == "" {
		return "***@" + domain
	}
	return string(local[0]) + "***@" + domain
}

// RedactToken returns a fixed placeholder for secrets/tokens.
func RedactToken(_ string) string { return "[redacted]" }
