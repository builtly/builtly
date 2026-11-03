package appstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	prodBase    = "https://api.storekit.itunes.apple.com"
	sandboxBase = "https://api.storekit-sandbox.itunes.apple.com"
)

// Config is apple_iap connection settings + secrets.
type Config struct {
	IssuerID      string
	KeyID         string
	BundleID      string
	PrivateKeyPEM string
}

// Transaction is a decoded App Store transaction payload.
type Transaction struct {
	TransactionID   string `json:"transactionId"`
	OriginalTxID    string `json:"originalTransactionId"`
	BundleID        string `json:"bundleId"`
	ProductID       string `json:"productId"`
	Type            string `json:"type"`
	Environment     string `json:"environment"` // Production | Sandbox
	AppAccountToken string `json:"appAccountToken"`
	RevocationDate  *int64 `json:"revocationDate"`
	SignedDate      int64  `json:"signedDate"`
	// Price is the recorded price in milliunits (major units × 1000). Nil when Apple omits it.
	Price    *int64 `json:"price"`
	Currency string `json:"currency"`
	Quantity int64  `json:"quantity"`
}

// NotificationPayload is the decoded App Store Server Notification V2 body.
type NotificationPayload struct {
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	NotificationUUID string `json:"notificationUUID"`
	Data             struct {
		BundleID              string `json:"bundleId"`
		Environment           string `json:"environment"`
		SignedTransactionInfo string `json:"signedTransactionInfo"`
	} `json:"data"`
}

// Client talks to the App Store Server API.
type Client struct {
	HTTP     *http.Client
	ProdURL  string
	Sandbox  string
	Verifier *Verifier

	mu     sync.Mutex
	cached string
	exp    time.Time
	cacheK string
}

// NewClient constructs a Client.
func NewClient(hc *http.Client, v *Verifier) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{HTTP: hc, ProdURL: prodBase, Sandbox: sandboxBase, Verifier: v}
}

// GetTransaction fetches and verifies a transaction by id.
func (c *Client) GetTransaction(ctx context.Context, cfg Config, transactionID string, allowSandbox bool) (Transaction, error) {
	tok, err := c.bearer(cfg)
	if err != nil {
		return Transaction{}, err
	}
	tx, status, err := c.fetch(ctx, c.ProdURL, tok, transactionID)
	if err == nil {
		return tx, nil
	}
	// Before the first App Store release, production returns 401 (often an empty body)
	// for a sandbox transaction id instead of 404 TransactionIdNotFound. Callers who
	// may not use sandbox are not sent to the sandbox host.
	if allowSandbox && (status == http.StatusNotFound || status == http.StatusUnauthorized) {
		return c.fetchOnly(ctx, c.Sandbox, tok, transactionID)
	}
	return Transaction{}, err
}

func (c *Client) fetchOnly(ctx context.Context, base, tok, id string) (Transaction, error) {
	tx, _, err := c.fetch(ctx, base, tok, id)
	return tx, err
}

func (c *Client) fetch(ctx context.Context, base, bearer, id string) (Transaction, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/inApps/v1/transactions/"+id, nil)
	if err != nil {
		return Transaction{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Transaction{}, 0, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return Transaction{}, res.StatusCode, &APIError{Status: res.StatusCode, Body: string(body)}
	}
	var wrap struct {
		SignedTransactionInfo string `json:"signedTransactionInfo"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return Transaction{}, res.StatusCode, err
	}
	tx, err := c.DecodeTransaction(wrap.SignedTransactionInfo)
	return tx, res.StatusCode, err
}

// DecodeTransaction verifies JWS and unmarshals Transaction.
func (c *Client) DecodeTransaction(signed string) (Transaction, error) {
	payload, err := c.Verifier.VerifyJWS(signed)
	if err != nil {
		return Transaction{}, err
	}
	var tx Transaction
	if err := json.Unmarshal(payload, &tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

// DecodeNotification verifies the outer signedPayload and returns the notification.
func (c *Client) DecodeNotification(signedPayload string) (NotificationPayload, error) {
	payload, err := c.Verifier.VerifyJWS(signedPayload)
	if err != nil {
		return NotificationPayload{}, err
	}
	var n NotificationPayload
	if err := json.Unmarshal(payload, &n); err != nil {
		return NotificationPayload{}, err
	}
	return n, nil
}

func (c *Client) bearer(cfg Config) (string, error) {
	key := cfg.IssuerID + "|" + cfg.KeyID + "|" + cfg.BundleID
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != "" && c.cacheK == key && time.Now().Before(c.exp) {
		return c.cached, nil
	}
	priv, err := parseECPrivate(cfg.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": cfg.IssuerID,
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
		"aud": "appstoreconnect-v1",
		"bid": cfg.BundleID,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	t.Header["kid"] = cfg.KeyID
	signed, err := t.SignedString(priv)
	if err != nil {
		return "", err
	}
	c.cached = signed
	c.cacheK = key
	c.exp = now.Add(4 * time.Minute)
	return signed, nil
}

func parseECPrivate(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("appstore: invalid PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ec, ok := key.(*ecdsa.PrivateKey); ok {
			return ec, nil
		}
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// APIError is a non-2xx StoreKit response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("appstore api %d: %s", e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
