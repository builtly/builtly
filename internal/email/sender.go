package email

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

// Message is a transactional email.
type Message struct {
	From, ReplyTo, To, Subject, HTML, Text string
	IdempotencyKey                         string
}

// Sender sends email via a provider connection.
type Sender interface {
	Send(ctx context.Context, conn integration.Resolved, msg Message) (providerID string, err error)
}

// ResendClient talks to api.resend.com.
type ResendClient struct {
	HTTP *http.Client
	Base string // default https://api.resend.com; override in tests
}

// NewResendClient constructs a Resend client.
func NewResendClient(hc *http.Client) *ResendClient {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &ResendClient{HTTP: hc, Base: "https://api.resend.com"}
}

// Send implements Sender.
func (c *ResendClient) Send(ctx context.Context, conn integration.Resolved, msg Message) (string, error) {
	var sec integration.APIKeySecrets
	if err := json.Unmarshal(conn.Secrets, &sec); err != nil || sec.APIKey == "" {
		return "", outbox.ErrPermanent{Err: fmt.Errorf("invalid resend secrets")}
	}
	body := map[string]any{
		"from":    msg.From,
		"to":      []string{msg.To},
		"subject": msg.Subject,
		"html":    msg.HTML,
		"text":    msg.Text,
	}
	if msg.ReplyTo != "" {
		body["reply_to"] = msg.ReplyTo
	}
	raw, _ := json.Marshal(body)
	base := c.Base
	if base == "" {
		base = "https://api.resend.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/emails", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+sec.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if msg.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", msg.IdempotencyKey)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", outbox.ErrRetryable{Err: err}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		return "", outbox.ErrRetryable{Err: fmt.Errorf("resend %d: %s", res.StatusCode, truncate(string(b), 200))}
	}
	if res.StatusCode >= 400 {
		return "", outbox.ErrPermanent{Err: fmt.Errorf("resend %d: %s", res.StatusCode, truncate(string(b), 200))}
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &out)
	return out.ID, nil
}

// MemorySender records messages for tests.
type MemorySender struct {
	Messages []Message
}

// Send implements Sender.
func (m *MemorySender) Send(_ context.Context, _ integration.Resolved, msg Message) (string, error) {
	m.Messages = append(m.Messages, msg)
	return "mem_" + msg.IdempotencyKey, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}
