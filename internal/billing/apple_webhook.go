package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/billing/appstore"
	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/platform/logx"
	"github.com/builtly/builtly/internal/platform/secrets"
)

// HandleAppleWebhook handles POST /webhooks/apple/{connection_id}.
func (s *Service) HandleAppleWebhook(w http.ResponseWriter, r *http.Request) {
	connectionID := chi.URLParam(r, "connection_id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var req struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.SignedPayload == "" {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	cfg, err := s.loadAppleIAPConnection(r.Context(), connectionID)
	if err != nil {
		if errors.Is(err, integration.ErrConnectionNotFound) || errors.Is(err, integration.ErrNotConfigured) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	notif, err := s.iap.apple.DecodeNotification(req.SignedPayload)
	if err != nil {
		logx.From(r.Context()).Warn("apple jws failed", "err", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	now := db.Now()
	ev := WebhookEvent{
		ID: uuid.Must(uuid.NewV7()), Provider: "apple", ConnectionID: connectionID,
		EventID: notif.NotificationUUID, EventType: notif.NotificationType,
		Status: WebhookReceived, Attempts: 1, ReceivedAt: now,
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

	boundApp, err := s.resolver.AppForConnection(r.Context(), integration.ProviderAppleIAP, connectionID)
	if err != nil {
		_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, WebhookFailed, "no bound app", nil, db.Now())
		w.WriteHeader(http.StatusOK)
		return
	}
	appID := &boundApp

	if notif.Data.BundleID != cfg.BundleID {
		_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, WebhookIgnored, "wrong bundle", appID, db.Now())
		w.WriteHeader(http.StatusOK)
		return
	}
	// Sandbox notifications are not dropped when allow_iap_sandbox is off. A refund of a
	// purchase already accepted via the email allowlist must still claw back. A sandbox
	// notification with no purchase row is ignored by handleAppleNotification.
	status, lastErr, transient := s.handleAppleNotification(r.Context(), boundApp, notif)
	if transient {
		_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, WebhookFailed, lastErr, appID, db.Now())
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	_ = s.store.FinishWebhookEvent(r.Context(), nil, row.ID, status, lastErr, appID, db.Now())
	w.WriteHeader(http.StatusOK)
}

func (s *Service) handleAppleNotification(ctx context.Context, appID uuid.UUID, notif appstore.NotificationPayload) (status, lastErr string, transient bool) {
	switch notif.NotificationType {
	case "REFUND", "REFUND_REVERSED":
		// ok
	default:
		return WebhookIgnored, "", false
	}
	tx, err := s.iap.apple.DecodeTransaction(notif.Data.SignedTransactionInfo)
	if err != nil {
		return WebhookFailed, err.Error(), false
	}
	p, err := s.store.GetPurchaseByProviderRef(ctx, nil, appID, ProviderApple, tx.TransactionID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return WebhookIgnored, "purchase not found", false
		}
		return WebhookFailed, err.Error(), true
	}
	switch notif.NotificationType {
	case "REFUND":
		key := "refund:apple:" + tx.TransactionID
		err = s.db.WithTx(ctx, func(sqlTx *sqlx.Tx) error {
			cur, err := s.store.GetPurchaseByID(ctx, sqlTx, p.AppID, p.ID)
			if err != nil {
				return err
			}
			return s.applyRefund(ctx, sqlTx, cur, cur.Credits, key, "apple_refund")
		})
	case "REFUND_REVERSED":
		key := "refund_reversal:apple:" + tx.TransactionID + ":" + notif.NotificationUUID
		err = s.db.WithTx(ctx, func(sqlTx *sqlx.Tx) error {
			cur, err := s.store.GetPurchaseByID(ctx, sqlTx, p.AppID, p.ID)
			if err != nil {
				return err
			}
			return s.applyRefundReversal(ctx, sqlTx, cur, cur.RefundedCredits, key, "apple_refund_reversed")
		})
	}
	if err != nil {
		return WebhookFailed, err.Error(), true
	}
	return WebhookProcessed, "", false
}

func (s *Service) loadAppleIAPConnection(ctx context.Context, connectionID string) (appstore.Config, error) {
	var settings map[string]any
	var plain []byte
	if connectionID == integration.DefaultConnectionID {
		c, sec, ok := s.integ.Env().Get(integration.ProviderAppleIAP)
		if !ok {
			return appstore.Config{}, integration.ErrNotConfigured
		}
		plain = sec
		_ = json.Unmarshal(c.Settings, &settings)
	} else {
		c, err := s.iStore.GetConnection(ctx, nil, connectionID)
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				return appstore.Config{}, integration.ErrConnectionNotFound
			}
			return appstore.Config{}, err
		}
		if c.Provider != integration.ProviderAppleIAP {
			return appstore.Config{}, integration.ErrConnectionNotFound
		}
		plain, err = s.box.Open(c.SecretsEnc, c.KeyVersion, secrets.ConnectionAAD(c.ID, c.Provider))
		if err != nil {
			return appstore.Config{}, err
		}
		_ = json.Unmarshal(c.Settings, &settings)
	}
	var sec integration.PEMSecrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return appstore.Config{}, err
	}
	return appstore.Config{
		IssuerID: str(settings["issuer_id"]), KeyID: str(settings["key_id"]),
		BundleID: str(settings["bundle_id"]), PrivateKeyPEM: sec.PrivateKeyPEM,
	}, nil
}
