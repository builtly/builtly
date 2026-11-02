package billing

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/builtly/builtly/internal/apperr"
	"github.com/builtly/builtly/internal/platform/httpx"
	"github.com/builtly/builtly/internal/principal"
	"github.com/builtly/builtly/internal/tenant"
)

// Handler serves checkout endpoints.
type Handler struct {
	svc *Service
}

// NewHandler constructs a billing Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// CheckoutStripe handles POST /checkout/stripe.
func (h *Handler) CheckoutStripe(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.IsAPIKey || p.UserID == uuid.Nil {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		ProductID    string `json:"product_id"`
		SuccessURL   string `json:"success_url"`
		CancelURL    string `json:"cancel_url"`
		AffiliateRef string `json:"affiliate_ref"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pid, err := uuid.Parse(body.ProductID)
	if err != nil {
		httpx.WriteError(w, r, ErrValidationFailed.WithDetails(map[string]any{"reason": "invalid product_id"}))
		return
	}
	resp, err := h.svc.CreateStripeCheckout(r.Context(), app, p.UserID, CheckoutInput{
		ProductID: pid, SuccessURL: body.SuccessURL, CancelURL: body.CancelURL, AffiliateRef: body.AffiliateRef,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

// StripeWebhook handles POST /webhooks/stripe/{connection_id}.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	h.svc.HandleStripeWebhook(w, r)
}

// AppleWebhook handles POST /webhooks/apple/{connection_id}.
func (h *Handler) AppleWebhook(w http.ResponseWriter, r *http.Request) {
	h.svc.HandleAppleWebhook(w, r)
}

// VerifyIAP handles POST /billing/verify-iap.
func (h *Handler) VerifyIAP(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.IsAPIKey || p.UserID == uuid.Nil {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body struct {
		Platform      string `json:"platform"`
		TransactionID string `json:"transaction_id"`
		ProductID     string `json:"product_id"`
		PurchaseToken string `json:"purchase_token"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.svc.VerifyIAP(r.Context(), app, p.UserID, VerifyIAPInput{
		Platform: body.Platform, TransactionID: body.TransactionID,
		ProductID: body.ProductID, PurchaseToken: body.PurchaseToken,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// TransferGuestCredits handles POST /billing/transfer-guest-credits.
func (h *Handler) TransferGuestCredits(w http.ResponseWriter, r *http.Request) {
	app, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	p, ok := principal.FromContext(r.Context())
	if !ok || p.IsAPIKey || p.UserID == uuid.Nil {
		httpx.WriteError(w, r, apperr.Unauthorized)
		return
	}
	var body TransferGuestCreditsInput
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res, err := h.svc.TransferGuestCredits(r.Context(), app, p.UserID, body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

