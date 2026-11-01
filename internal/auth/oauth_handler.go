package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/tenant"
)

// OAuthStart handles GET /auth/oauth/{provider}.
func (h *Handler) OAuthStart(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	appID, err := uuid.Parse(r.URL.Query().Get("app_id"))
	if err != nil {
		http.Error(w, "bad app_id", http.StatusBadRequest)
		return
	}
	redirectURI := r.URL.Query().Get("redirect_uri")
	challenge := r.URL.Query().Get("code_challenge")
	method := r.URL.Query().Get("code_challenge_method")
	if method == "" {
		method = "S256"
	}
	attr := map[string]any{}
	if v := r.URL.Query().Get("affiliate_ref"); v != "" {
		attr["affiliate_ref"] = v
	}
	for _, k := range []string{"utm_source", "utm_medium", "utm_campaign"} {
		if v := r.URL.Query().Get(k); v != "" {
			attr[k] = v
		}
	}
	attrJSON, _ := json.Marshal(attr)
	url, err := h.svc.StartOAuth(r.Context(), providerName, appID, redirectURI, challenge, method, attrJSON, r.URL.Query().Get("referral_code"))
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Status < 500 {
			http.Error(w, ae.Message, ae.Status)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// OAuthCallback handles GET|POST /auth/oauth/{provider}/callback.
func (h *Handler) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	providerName := chi.URLParam(r, "provider")
	_ = r.ParseForm()
	code := firstNonEmpty(r.FormValue("code"), r.URL.Query().Get("code"))
	state := firstNonEmpty(r.FormValue("state"), r.URL.Query().Get("state"))
	errCode := firstNonEmpty(r.FormValue("error"), r.URL.Query().Get("error"))
	appleName := ""
	if userJSON := r.FormValue("user"); userJSON != "" {
		var u struct {
			Name struct {
				FirstName string `json:"firstName"`
				LastName  string `json:"lastName"`
			} `json:"name"`
		}
		if json.Unmarshal([]byte(userJSON), &u) == nil {
			appleName = strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
		}
	}
	redirect, err := h.svc.HandleOAuthCallback(r.Context(), providerName, code, state, errCode, appleName)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// OAuthExchange handles POST /auth/oauth/exchange.
func (h *Handler) OAuthExchange(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"code_verifier"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.ExchangeLoginCode(r.Context(), app, body.Code, body.CodeVerifier, r.UserAgent(), h.clientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// AppleNative handles POST /auth/apple.
func (h *Handler) AppleNative(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		IdentityToken     string `json:"identity_token"`
		AuthorizationCode string `json:"authorization_code"`
		RawNonce          string `json:"raw_nonce"`
		FullName          *struct {
			GivenName  string `json:"given_name"`
			FamilyName string `json:"family_name"`
		} `json:"full_name"`
		ReferralCode string          `json:"referral_code"`
		Attribution  json.RawMessage `json:"attribution"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	full := ""
	if body.FullName != nil {
		full = strings.TrimSpace(body.FullName.GivenName + " " + body.FullName.FamilyName)
	}
	resp, err := h.svc.NativeApple(r.Context(), app, body.IdentityToken, body.AuthorizationCode, body.RawNonce, full, body.Attribution, body.ReferralCode, r.UserAgent(), h.clientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// GoogleNative handles POST /auth/google.
func (h *Handler) GoogleNative(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		IDToken      string          `json:"id_token"`
		RawNonce     string          `json:"raw_nonce"`
		ReferralCode string          `json:"referral_code"`
		Attribution  json.RawMessage `json:"attribution"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.NativeGoogle(r.Context(), app, body.IDToken, body.RawNonce, body.Attribution, body.ReferralCode, r.UserAgent(), h.clientIP(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// PublicConfig handles GET /auth/config.
func (h *Handler) PublicConfig(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	cfg, err := h.svc.PublicConfig(r.Context(), app)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, cfg)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
