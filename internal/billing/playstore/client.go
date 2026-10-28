package playstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const scope = "https://www.googleapis.com/auth/androidpublisher"

// Config is google_play connection settings + secrets.
type Config struct {
	PackageName        string
	ServiceAccountJSON []byte
}

// ProductPurchase is a subset of the Play Developer API resource.
type ProductPurchase struct {
	PurchaseState               int    `json:"purchaseState"` // 0 purchased
	ConsumptionState            int    `json:"consumptionState"`
	PurchaseType                *int   `json:"purchaseType"` // 0 test
	ObfuscatedExternalAccountID string `json:"obfuscatedExternalAccountId"`
	OrderID                     string `json:"orderId"`
	PurchaseToken               string `json:"purchaseToken"`
}

// VoidedPurchase is one voided purchase list item.
type VoidedPurchase struct {
	PurchaseToken      string `json:"purchaseToken"`
	PurchaseTimeMillis string `json:"purchaseTimeMillis"`
	VoidedTimeMillis   string `json:"voidedTimeMillis"`
	OrderID            string `json:"orderId"`
}

// Client talks to the Google Play Developer API.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	// TokenSource override for tests.
	TokenSource oauth2.TokenSource
}

// New constructs a Client.
func New(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{HTTP: hc, BaseURL: "https://androidpublisher.googleapis.com"}
}

// GetProductPurchase fetches a one-time product purchase.
func (c *Client) GetProductPurchase(ctx context.Context, cfg Config, productID, token string) (ProductPurchase, error) {
	hc, err := c.httpClient(ctx, cfg)
	if err != nil {
		return ProductPurchase{}, err
	}
	path := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/purchases/products/%s/tokens/%s",
		strings.TrimRight(c.BaseURL, "/"), url.PathEscape(cfg.PackageName), url.PathEscape(productID), url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return ProductPurchase{}, err
	}
	res, err := hc.Do(req)
	if err != nil {
		return ProductPurchase{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return ProductPurchase{}, &APIError{Status: res.StatusCode, Body: string(body)}
	}
	var p ProductPurchase
	if err := json.Unmarshal(body, &p); err != nil {
		return ProductPurchase{}, err
	}
	return p, nil
}

// Consume acknowledges a consumable purchase.
func (c *Client) Consume(ctx context.Context, cfg Config, productID, token string) error {
	hc, err := c.httpClient(ctx, cfg)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/purchases/products/%s/tokens/%s:consume",
		strings.TrimRight(c.BaseURL, "/"), url.PathEscape(cfg.PackageName), url.PathEscape(productID), url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return &APIError{Status: res.StatusCode, Body: string(b)}
	}
	return nil
}

// ListVoidedPurchases lists voided purchases since startTimeMillis.
func (c *Client) ListVoidedPurchases(ctx context.Context, cfg Config, startTimeMillis int64, pageToken string) ([]VoidedPurchase, string, error) {
	hc, err := c.httpClient(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	q := url.Values{}
	q.Set("startTime", fmt.Sprintf("%d", startTimeMillis))
	q.Set("type", "0")
	if pageToken != "" {
		q.Set("token", pageToken)
	}
	path := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/purchases/voidedpurchases?%s",
		strings.TrimRight(c.BaseURL, "/"), url.PathEscape(cfg.PackageName), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return nil, "", &APIError{Status: res.StatusCode, Body: string(body)}
	}
	var out struct {
		VoidedPurchases []VoidedPurchase `json:"voidedPurchases"`
		TokenPagination struct {
			NextPageToken string `json:"nextPageToken"`
		} `json:"tokenPagination"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", err
	}
	return out.VoidedPurchases, out.TokenPagination.NextPageToken, nil
}

func (c *Client) httpClient(ctx context.Context, cfg Config) (*http.Client, error) {
	src := c.TokenSource
	if src == nil {
		creds, err := google.CredentialsFromJSON(ctx, cfg.ServiceAccountJSON, scope)
		if err != nil {
			return nil, fmt.Errorf("playstore: credentials: %w", err)
		}
		src = creds.TokenSource
	}
	// Use oauth2.Transport directly. oauth2.NewClient wraps the source in
	// ReuseTokenSource, which mutates *Token (expiryDelta) and races when
	// callers share a StaticTokenSource across concurrent HTTP clients.
	base := http.DefaultTransport
	timeout := 15 * time.Second
	if c.HTTP != nil {
		if c.HTTP.Transport != nil {
			base = c.HTTP.Transport
		}
		if c.HTTP.Timeout > 0 {
			timeout = c.HTTP.Timeout
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &oauth2.Transport{
			Base:   base,
			Source: src,
		},
	}, nil
}

// APIError is a non-2xx Play API response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := e.Body
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("playstore api %d: %s", e.Status, msg)
}
