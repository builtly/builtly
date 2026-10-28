package stripeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a minimal Stripe REST client (no stripe-go).
type Client struct {
	HTTP    *http.Client
	BaseURL string // default https://api.stripe.com
}

// New constructs a Client.
func New(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{HTTP: hc, BaseURL: "https://api.stripe.com"}
}

// Account is GET /v1/account.
type Account struct {
	ID string `json:"id"`
}

// Price is a subset of GET /v1/prices/{id}.
type Price struct {
	ID       string `json:"id"`
	Active   bool   `json:"active"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
}

// CheckoutSession is the create-session response.
type CheckoutSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// GetAccount calls GET /v1/account.
func (c *Client) GetAccount(ctx context.Context, secretKey string) (Account, error) {
	var acc Account
	err := c.do(ctx, secretKey, http.MethodGet, "/v1/account", nil, "", &acc)
	return acc, err
}

// GetPrice calls GET /v1/prices/{id}.
func (c *Client) GetPrice(ctx context.Context, secretKey, priceID string) (Price, error) {
	var p Price
	err := c.do(ctx, secretKey, http.MethodGet, "/v1/prices/"+url.PathEscape(priceID), nil, "", &p)
	return p, err
}

// CreateCheckoutSession posts form-encoded checkout session params.
func (c *Client) CreateCheckoutSession(ctx context.Context, secretKey string, form url.Values, idempotencyKey string) (CheckoutSession, error) {
	var s CheckoutSession
	err := c.do(ctx, secretKey, http.MethodPost, "/v1/checkout/sessions", form, idempotencyKey, &s)
	return s, err
}

func (c *Client) do(ctx context.Context, secretKey, method, path string, form url.Values, idem string, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.stripe.com"
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(secretKey, "")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		return &APIError{Status: res.StatusCode, Body: string(b)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// APIError is a Stripe HTTP error.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("stripe api %d: %s", e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
