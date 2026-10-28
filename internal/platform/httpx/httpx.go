// Package httpx provides JSON request/response helpers and error rendering.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/logx"
)

const maxJSONBody = 64 << 10 // 64 KiB

// DecodeJSON decodes a single JSON value from r.Body with size and content-type checks.
func DecodeJSON(r *http.Request, dst any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return apperr.New(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apperr.New(http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
		}
		return apperr.New(http.StatusBadRequest, "invalid_json", "invalid JSON body")
	}
	// Reject trailing junk.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return apperr.New(http.StatusBadRequest, "invalid_json", "request body must contain a single JSON value")
	}
	return nil
}

// WriteJSON writes status and v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError renders err: *apperr.Error as its status/code; anything else as 500 internal.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		WriteJSON(w, ae.Status, map[string]any{
			"error": map[string]any{
				"code":    ae.Code,
				"message": ae.Message,
				"details": ae.Details,
			},
		})
		return
	}
	logx.From(r.Context()).Error("request failed", slog.String("err", err.Error()))
	WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]any{
			"code":    "internal",
			"message": "an unexpected error occurred",
		},
	})
}

// ClientIP returns the client IP, honoring X-Forwarded-For only from trusted proxies.
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !isTrusted(ip, trusted) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		cand := net.ParseIP(p)
		if cand == nil {
			continue
		}
		if !isTrusted(cand, trusted) {
			return p
		}
	}
	return strings.TrimSpace(parts[0])
}

func isTrusted(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
