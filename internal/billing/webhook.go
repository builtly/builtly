package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/billing/stripeapi"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// HandleStripeWebhook handles POST /webhooks/stripe/{connection_id}.
func (s *Service) HandleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	connectionID := chi.URLParam(r, "connection_id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	sec, settings, err := s.loadStripeConnection(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, integration.ErrConnectionNotFound) || errors.Is(err, integration.ErrNotConfigured) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		logx.From(r.Context()).Error("stripe webhook resolve", "err", err)
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	if err := stripeapi.VerifySignature(r.Header.Get("Stripe-Signature"), body, sec.WebhookSecrets, time.Now().UTC()); err != nil {
		logx.From(r.Context()).Warn("stripe signature failed", "err", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var evt stripeEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	livemode, _ := settings["livemode"].(bool)
	if evt.Livemode != livemode {
		http.Error(w, "livemode mismatch", http.StatusBadRequest)
		return
	}

	now := db.Now()
	ev := WebhookEvent{
		ID: uuid.Must(uuid.NewV7()), Provider: ProviderStripe, ConnectionID: connectionID,
		EventID: evt.ID, EventType: evt.Type, Status: WebhookReceived, Attempts: 1, ReceivedAt: now,
	}
	row, inserted, err := s.store.TryInsertWebhookEvent(r.Context(), nil, ev)
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	if !inserted {
		if row.Status == WebhookProcessed || row.Status == WebhookIgnored {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = s.store.BumpWebhookAttempt(r.Context(), nil, row.ID, "")
	}

	status, lastErr, appID, transient := s.dispatchStripe(r.Context(), connectionID, row.ID, evt, logx.From(r.Context()))
	if transient {
		_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, WebhookFailed, lastErr, appID, db.Now())
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, status, lastErr, appID, db.Now())
	w.WriteHeader(http.StatusOK)
}

func (s *Service) loadStripeConnection(ctx context.Context, connectionID string) (integration.StripeSecrets, map[string]any, error) {
	settings := map[string]any{}
	var plain []byte
	if connectionID == integration.DefaultConnectionID {
		c, sec, ok := s.integ.Env().Get(integration.ProviderStripe)
		if !ok {
			return integration.StripeSecrets{}, nil, integration.ErrNotConfigured
		}
		plain = sec
		_ = json.Unmarshal(c.Settings, &settings)
	} else {
		c, err := s.iStore.GetConnection(ctx, nil, connectionID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return integration.StripeSecrets{}, nil, integration.ErrConnectionNotFound
			}
			return integration.StripeSecrets{}, nil, err
		}
		if c.Provider != integration.ProviderStripe {
			return integration.StripeSecrets{}, nil, integration.ErrConnectionNotFound
		}
		plain, err = s.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return integration.StripeSecrets{}, nil, err
		}
		_ = json.Unmarshal(c.Settings, &settings)
	}
	var sec integration.StripeSecrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return integration.StripeSecrets{}, nil, err
	}
	return sec, settings, nil
}

type stripeEvent struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Livemode bool            `json:"livemode"`
	Data     json.RawMessage `json:"data"`
}

type stripeDataObject struct {
	Object json.RawMessage `json:"object"`
}

func (s *Service) dispatchStripe(ctx context.Context, connectionID string, webhookID uuid.UUID, evt stripeEvent, log *slog.Logger) (status, lastErr string, appID *uuid.UUID, transient bool) {
	_ = webhookID
	boundApp, err := s.resolver.AppForConnection(ctx, integration.ProviderStripe, connectionID)
	if err != nil {
		log.Error("stripe webhook no bound app", "connection_id", connectionID, "err", err)
		return WebhookFailed, "no bound app", nil, false
	}
	appID = &boundApp

	var data stripeDataObject
	_ = json.Unmarshal(evt.Data, &data)

	switch evt.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var sess checkoutSessionObj
		if err := json.Unmarshal(data.Object, &sess); err != nil {
			return WebhookFailed, err.Error(), appID, false
		}
		if metaApp := sess.Metadata["app_id"]; metaApp != "" && metaApp != boundApp.String() {
			return WebhookIgnored, "app_id mismatch", appID, false
		}
		if evt.Type == "checkout.session.completed" && sess.PaymentStatus == "unpaid" {
			return WebhookProcessed, "", appID, false
		}
		if sess.PaymentStatus != "paid" && evt.Type == "checkout.session.completed" {
			return WebhookProcessed, "", appID, false
		}
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			return s.fulfilCheckout(ctx, tx, boundApp, sess.ID, sess.AmountTotal, sess.Currency, sess.PaymentIntent, sess.Metadata["purchase_id"])
		})
		if err != nil {
			log.Error("stripe fulfil failed", "err", err)
			return WebhookFailed, err.Error(), appID, true
		}
		return WebhookProcessed, "", appID, false

	case "checkout.session.async_payment_failed":
		var sess checkoutSessionObj
		_ = json.Unmarshal(data.Object, &sess)
		if metaApp := sess.Metadata["app_id"]; metaApp != "" && metaApp != boundApp.String() {
			return WebhookIgnored, "app_id mismatch", appID, false
		}
		_ = s.store.MarkPurchaseStatus(ctx, nil, boundApp, mustParseUUID(sess.Metadata["purchase_id"]), StatusPending, StatusFailed, db.Now())
		if sess.ID != "" {
			if p, err := s.store.GetPurchaseByProviderRef(ctx, nil, boundApp, ProviderStripe, sess.ID); err == nil {
				_ = s.store.MarkPurchaseStatus(ctx, nil, boundApp, p.ID, StatusPending, StatusFailed, db.Now())
			}
		}
		return WebhookProcessed, "", appID, false

	case "checkout.session.expired":
		var sess checkoutSessionObj
		_ = json.Unmarshal(data.Object, &sess)
		if metaApp := sess.Metadata["app_id"]; metaApp != "" && metaApp != boundApp.String() {
			return WebhookIgnored, "app_id mismatch", appID, false
		}
		if p, err := s.store.GetPurchaseByProviderRef(ctx, nil, boundApp, ProviderStripe, sess.ID); err == nil {
			_ = s.store.MarkPurchaseStatus(ctx, nil, boundApp, p.ID, StatusPending, StatusExpired, db.Now())
		}
		return WebhookProcessed, "", appID, false

	case "charge.refunded":
		var ch chargeObj
		if err := json.Unmarshal(data.Object, &ch); err != nil {
			return WebhookFailed, err.Error(), appID, false
		}
		p, err := s.store.GetPurchaseByPaymentRef(ctx, nil, ProviderStripe, ch.PaymentIntent)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return WebhookIgnored, "purchase not found", appID, false
			}
			return WebhookFailed, err.Error(), appID, true
		}
		if p.AppID != boundApp {
			return WebhookIgnored, "app mismatch", appID, false
		}
		target := refundCredits(p, ch.AmountRefunded, ch.Amount)
		key := fmt.Sprintf("refund:stripe:%s:%d", p.ID.String(), target)
		err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			cur, err := s.store.GetPurchaseByID(ctx, tx, p.AppID, p.ID)
			if err != nil {
				return err
			}
			return s.applyRefund(ctx, tx, cur, target, key, "stripe_refund")
		})
		if err != nil {
			return WebhookFailed, err.Error(), appID, true
		}
		return WebhookProcessed, "", appID, false

	case "charge.dispute.created":
		var d disputeObj
		if err := json.Unmarshal(data.Object, &d); err != nil {
			return WebhookFailed, err.Error(), appID, false
		}
		p, err := s.store.GetPurchaseByPaymentRef(ctx, nil, ProviderStripe, d.PaymentIntent)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return WebhookIgnored, "purchase not found", appID, false
			}
			return WebhookFailed, err.Error(), appID, true
		}
		if p.AppID != boundApp {
			return WebhookIgnored, "app mismatch", appID, false
		}
		key := "refund:stripe-dispute:" + d.ID
		err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			cur, err := s.store.GetPurchaseByID(ctx, tx, p.AppID, p.ID)
			if err != nil {
				return err
			}
			return s.applyRefund(ctx, tx, cur, cur.Credits, key, "stripe_dispute")
		})
		if err != nil {
			return WebhookFailed, err.Error(), appID, true
		}
		return WebhookProcessed, "", appID, false

	case "charge.dispute.closed":
		var d disputeObj
		if err := json.Unmarshal(data.Object, &d); err != nil {
			return WebhookFailed, err.Error(), appID, false
		}
		if d.Status != "won" {
			return WebhookIgnored, "dispute not won", appID, false
		}
		p, err := s.store.GetPurchaseByPaymentRef(ctx, nil, ProviderStripe, d.PaymentIntent)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return WebhookIgnored, "purchase not found", appID, false
			}
			return WebhookFailed, err.Error(), appID, true
		}
		if p.AppID != boundApp {
			return WebhookIgnored, "app mismatch", appID, false
		}
		key := "refund_reversal:stripe-dispute:" + d.ID
		err = s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			cur, err := s.store.GetPurchaseByID(ctx, tx, p.AppID, p.ID)
			if err != nil {
				return err
			}
			// Reverse full dispute clawback (credits).
			return s.applyRefundReversal(ctx, tx, cur, cur.Credits, key, "stripe_dispute_won")
		})
		if err != nil {
			return WebhookFailed, err.Error(), appID, true
		}
		return WebhookProcessed, "", appID, false

	default:
		return WebhookIgnored, "", appID, false
	}
}

type checkoutSessionObj struct {
	ID            string            `json:"id"`
	PaymentStatus string            `json:"payment_status"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	PaymentIntent string            `json:"payment_intent"`
	Metadata      map[string]string `json:"metadata"`
}

type chargeObj struct {
	ID             string `json:"id"`
	PaymentIntent  string `json:"payment_intent"`
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
}

type disputeObj struct {
	ID            string `json:"id"`
	PaymentIntent string `json:"payment_intent"`
	Status        string `json:"status"`
}

func refundCredits(p Purchase, amountRefunded, chargeAmount int64) int64 {
	if p.Amount != nil && *p.Amount > 0 {
		chargeAmount = *p.Amount
	}
	if chargeAmount <= 0 {
		return p.Credits
	}
	if amountRefunded >= chargeAmount {
		return p.Credits
	}
	return int64(math.Floor(float64(p.Credits) * float64(amountRefunded) / float64(chargeAmount)))
}

func mustParseUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
