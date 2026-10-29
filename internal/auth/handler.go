package auth

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/platform/ratelimit"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// Handler serves /api/v1/auth/* and /user/me.
type Handler struct {
	svc           *Service
	registerLimit *ratelimit.Limiter
	loginIPLimit  *ratelimit.Limiter
	loginEmailLim *ratelimit.Limiter
	verifyLimit   *ratelimit.Limiter
	forgotLimit   *ratelimit.Limiter
	trusted       []*net.IPNet
}

// NewHandler constructs auth HTTP handlers.
func NewHandler(svc *Service, trusted []*net.IPNet) *Handler {
	return &Handler{
		svc:           svc,
		registerLimit: ratelimit.New(1, 10), // burst 10, ~1/min refill ≈ 10/h for practical abuse control
		loginIPLimit:  ratelimit.New(10, 10),
		loginEmailLim: ratelimit.New(5, 5),
		verifyLimit:   ratelimit.New(1, 3),
		forgotLimit:   ratelimit.New(5, 5),
		trusted:       trusted,
	}
}

func (h *Handler) clientIP(r *http.Request) string {
	return httpx.ClientIP(r, h.trusted)
}

func (h *Handler) app(r *http.Request) (tenant.App, bool) {
	return tenant.FromContext(r.Context())
}

// Register handles POST /auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	ip := h.clientIP(r)
	if !h.registerLimit.Allow(app.ID.String() + ":" + ip) {
		httpx.WriteError(w, r, ErrRateLimited)
		return
	}
	var body struct {
		Email        string          `json:"email"`
		Password     string          `json:"password"`
		DisplayName  string          `json:"display_name"`
		Locale       string          `json:"locale"`
		ReferralCode string          `json:"referral_code"`
		Attribution  json.RawMessage `json:"attribution"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	locale := body.Locale
	if locale == "" {
		locale = r.Header.Get("Accept-Language")
	}
	resp, err := h.svc.Register(r.Context(), app, RegisterInput{
		Email: body.Email, Password: body.Password, DisplayName: body.DisplayName,
		Locale: locale, ReferralCode: body.ReferralCode, Attribution: body.Attribution,
		UserAgent: r.UserAgent(), IP: ip,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

// Login handles POST /auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	ip := h.clientIP(r)
	if !h.loginIPLimit.Allow(app.ID.String() + ":" + ip) {
		httpx.WriteError(w, r, ErrRateLimited)
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.loginEmailLim.Allow(app.ID.String() + ":" + body.Email) {
		httpx.WriteError(w, r, ErrRateLimited)
		return
	}
	resp, err := h.svc.Login(r.Context(), app, LoginInput{
		Email: body.Email, Password: body.Password, UserAgent: r.UserAgent(), IP: ip,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Refresh handles POST /auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.Refresh(r.Context(), app, body.RefreshToken, r.UserAgent(), h.clientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Logout handles POST /auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = httpx.DecodeJSON(r, &body)
	h.svc.Logout(r.Context(), body.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

// RequestVerify handles POST /auth/email/verify/request.
func (h *Handler) RequestVerify(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.IsAPIKey {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	if !h.verifyLimit.Allow(p.UserID.String()) {
		httpx.WriteError(w, r, ErrRateLimited)
		return
	}
	if err := h.svc.RequestVerifyEmail(r.Context(), app, p.UserID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// VerifyEmail handles POST /auth/email/verify.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	u, err := h.svc.VerifyEmail(r.Context(), app, body.Token)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": u})
}

// ForgotPassword handles POST /auth/password/forgot.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	ip := h.clientIP(r)
	if !h.forgotLimit.Allow(app.ID.String() + ":" + ip) {
		httpx.WriteError(w, r, ErrRateLimited)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.svc.ForgotPassword(r.Context(), app, body.Email)
	w.WriteHeader(http.StatusAccepted)
}

// ResetPassword handles POST /auth/password/reset.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.svc.ResetPassword(r.Context(), app, body.Token, body.Password); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMe handles GET /user/me.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	me, err := h.svc.Me(r.Context(), app.ID, p.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

// PatchMe handles PATCH /user/me.
func (h *Handler) PatchMe(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		DisplayName *string `json:"display_name"`
		AvatarURL   *string `json:"avatar_url"`
		Locale      *string `json:"locale"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	me, err := h.svc.PatchMe(r.Context(), app.ID, p.UserID, body.DisplayName, body.AvatarURL, body.Locale)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

// CreateAvatar handles POST /user/me/avatar.
func (h *Handler) CreateAvatar(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.BeginAvatarUpload(r.Context(), app, p.UserID, body.Filename, body.ContentType, body.SizeBytes)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"file": map[string]any{
			"id": res.FileID, "filename": res.Filename,
			"content_type": res.ContentType, "size_bytes": res.SizeBytes,
		},
		"upload": map[string]any{
			"method": res.Upload.Method, "url": res.Upload.URL,
			"headers": res.Upload.Headers, "expires_at": res.Upload.ExpiresAt,
		},
	})
}

// CompleteAvatar handles POST /user/me/avatar/complete.
func (h *Handler) CompleteAvatar(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		FileID string `json:"file_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	fileID, err := uuid.Parse(body.FileID)
	if err != nil {
		httpx.WriteError(w, r, apperr.BadRequest.WithDetails(map[string]any{"reason": "invalid file_id"}))
		return
	}
	me, err := h.svc.CompleteAvatarUpload(r.Context(), app, p.UserID, fileID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, me)
}

// DeleteMe handles DELETE /user/me.
func (h *Handler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	app, ok := h.app(r)
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Password *string `json:"password"`
	}
	if r.ContentLength != 0 && r.Body != nil && r.Header.Get("Content-Type") != "" {
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := h.svc.DeleteMe(r.Context(), app.ID, p.UserID, body.Password, p.IssuedAt); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
