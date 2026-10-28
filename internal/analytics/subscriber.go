// Package analytics delivers PostHog events from the outbox.
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/builtly/builtly/internal/integration"
	"github.com/builtly/builtly/internal/outbox"
)

// Client posts events to PostHog.
type Client struct {
	HTTP    *http.Client
	BaseURL string // tests override; empty → connection host
}

// NewClient constructs a Client.
func NewClient(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{HTTP: hc}
}

// Subscriber handles analytics outbox rows.
type Subscriber struct {
	client   *Client
	resolver *integration.Resolver
}

// NewSubscriber constructs a Subscriber.
func NewSubscriber(client *Client, resolver *integration.Resolver) *Subscriber {
	return &Subscriber{client: client, resolver: resolver}
}

// Handle implements outbox.Handler.
func (s *Subscriber) Handle(ctx context.Context, row outbox.Row, raw []byte) error {
	if row.AppID == nil {
		return outbox.ErrPermanent{Err: fmt.Errorf("analytics: missing app_id")}
	}
	resolved, err := s.resolver.For(ctx, *row.AppID, integration.ProviderPostHog)
	if err == integration.ErrNotConfigured {
		return outbox.ErrNotConfigured
	}
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	var settings map[string]any
	_ = json.Unmarshal(resolved.Settings, &settings)
	host, _ := settings["host"].(string)
	if host == "" {
		host = "https://us.i.posthog.com"
	}
	var sec integration.PostHogSecrets
	if err := json.Unmarshal(resolved.Secrets, &sec); err != nil || sec.ProjectAPIKey == "" {
		return outbox.ErrNotConfigured
	}

	event, distinct, props, ok := mapEvent(row.EventType, raw)
	if !ok {
		return nil // nothing to capture
	}
	if props == nil {
		props = map[string]any{}
	}
	props["app_id"] = row.AppID.String()
	props["$lib"] = "builtly"

	body, _ := json.Marshal(map[string]any{
		"api_key":     sec.ProjectAPIKey,
		"event":       event,
		"distinct_id": distinct,
		"properties":  props,
		"timestamp":   time.Now().UTC().Format(time.RFC3339Nano),
		"uuid":        row.ID.String(),
	})
	base := s.client.BaseURL
	if base == "" {
		base = strings.TrimRight(host, "/")
	}
	url := base + "/i/v0/e/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return outbox.ErrPermanent{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.HTTP.Do(req)
	if err != nil {
		return outbox.ErrRetryable{Err: err}
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode >= 500 {
		return outbox.ErrRetryable{Err: fmt.Errorf("posthog %d", res.StatusCode)}
	}
	if res.StatusCode >= 300 {
		return outbox.ErrPermanent{Err: fmt.Errorf("posthog %d", res.StatusCode)}
	}
	return nil
}

func mapEvent(eventType string, raw []byte) (event, distinct string, props map[string]any, ok bool) {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	switch eventType {
	case outbox.EventUserRegistered:
		return "user_signed_up", str(m["user_id"]), map[string]any{}, m["user_id"] != nil
	case outbox.EventPurchaseCompleted:
		props = map[string]any{
			"product_id": str(m["product_id"]), "credits": m["credits"],
			"provider": str(m["provider"]), "amount": m["amount"], "currency": m["currency"],
		}
		return "purchase_completed", str(m["user_id"]), props, m["user_id"] != nil
	case outbox.EventPurchaseRefunded:
		props = map[string]any{
			"purchase_id": str(m["purchase_id"]), "delta": m["delta"], "full": m["full"],
			"source": str(m["source"]),
		}
		// refund payloads may lack user_id — use purchase_id as distinct fallback
		d := str(m["user_id"])
		if d == "" {
			d = str(m["purchase_id"])
		}
		return "purchase_refunded", d, props, d != ""
	case outbox.EventReferralRewarded:
		return "referral_rewarded", str(m["referrer_id"]), map[string]any{
			"credits": m["credits"], "referee_id": str(m["referee_id"]),
		}, m["referrer_id"] != nil
	case outbox.EventCreditsConsumed:
		return "credits_consumed", str(m["user_id"]), map[string]any{
			"amount": m["amount"], "feature": str(m["feature"]),
		}, m["user_id"] != nil
	case outbox.EventUserDeleted:
		return "user_deleted", str(m["user_id"]), map[string]any{}, m["user_id"] != nil
	default:
		return "", "", nil, false
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
