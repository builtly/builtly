package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/builtly/builtly/internal/platform/s3sig"
)

// R2Config is resolved connection settings + secrets.
type R2Config struct {
	AccountID       string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	KeyPrefix       string
	Endpoint        string // override for tests; default Cloudflare R2
}

// ObjectMeta is HEAD response data.
type ObjectMeta struct {
	ContentLength int64
	ContentType   string
}

// R2Client talks to Cloudflare R2 (S3 API).
type R2Client struct {
	HTTP *http.Client
}

// NewR2Client constructs an R2Client.
func NewR2Client(hc *http.Client) *R2Client {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &R2Client{HTTP: hc}
}

func (c *R2Client) endpoint(cfg R2Config) string {
	if cfg.Endpoint != "" {
		return strings.TrimRight(cfg.Endpoint, "/")
	}
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
}

func (c *R2Client) objectURL(cfg R2Config, key string) (*url.URL, error) {
	base := c.endpoint(cfg)
	u, err := url.Parse(base + "/" + cfg.Bucket + "/" + strings.TrimPrefix(key, "/"))
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (c *R2Client) creds(cfg R2Config) s3sig.Credentials {
	return s3sig.Credentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey}
}

// PresignPUT returns a presigned PUT URL and headers the client must send.
func (c *R2Client) PresignPUT(cfg R2Config, key, contentType string, expires time.Duration, now time.Time) (string, map[string]string, time.Time, error) {
	u, err := c.objectURL(cfg, key)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	hdrs := http.Header{}
	hdrs.Set("Content-Type", contentType)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	exp := now.Add(expires)
	signed := s3sig.Presign(s3sig.PresignInput{
		Method: "PUT", URL: u, Headers: hdrs,
		SignedHeaderKeys: []string{"host", "content-type"},
		Expires:          expires, Service: "s3", Region: "auto",
		Credentials: c.creds(cfg), Now: now,
	})
	return signed.String(), map[string]string{"Content-Type": contentType}, exp, nil
}

// PresignGET returns a short-lived download URL.
func (c *R2Client) PresignGET(cfg R2Config, key, filename string, expires time.Duration, now time.Time) (string, time.Time, error) {
	u, err := c.objectURL(cfg, key)
	if err != nil {
		return "", time.Time{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	q := u.Query()
	q.Set("response-content-disposition", contentDisposition(filename))
	u.RawQuery = q.Encode()
	// Re-parse so Presign sees disposition in query (will be signed as query param).
	signed := s3sig.Presign(s3sig.PresignInput{
		Method: "GET", URL: u, Headers: http.Header{},
		SignedHeaderKeys: []string{"host"},
		Expires:          expires, Service: "s3", Region: "auto",
		Credentials: c.creds(cfg), Now: now,
	})
	return signed.String(), now.Add(expires), nil
}

// HeadObject issues a signed HEAD.
func (c *R2Client) HeadObject(ctx context.Context, cfg R2Config, key string) (ObjectMeta, error) {
	u, err := c.objectURL(cfg, key)
	if err != nil {
		return ObjectMeta{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return ObjectMeta{}, err
	}
	s3sig.Sign(s3sig.SignInput{
		Method: http.MethodHead, URL: u, Headers: req.Header,
		Service: "s3", Region: "auto", Credentials: c.creds(cfg),
	})
	res, err := c.HTTP.Do(req)
	if err != nil {
		return ObjectMeta{}, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode == http.StatusNotFound {
		return ObjectMeta{}, errObjectNotFound
	}
	if res.StatusCode >= 300 {
		return ObjectMeta{}, fmt.Errorf("r2 head %d", res.StatusCode)
	}
	n, _ := strconv.ParseInt(res.Header.Get("Content-Length"), 10, 64)
	return ObjectMeta{ContentLength: n, ContentType: res.Header.Get("Content-Type")}, nil
}

// DeleteObject issues a signed DELETE.
func (c *R2Client) DeleteObject(ctx context.Context, cfg R2Config, key string) error {
	u, err := c.objectURL(cfg, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	s3sig.Sign(s3sig.SignInput{
		Method: http.MethodDelete, URL: u, Headers: req.Header,
		Service: "s3", Region: "auto", Credentials: c.creds(cfg),
	})
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
	if res.StatusCode == http.StatusNotFound || res.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("r2 delete %d", res.StatusCode)
}

var errObjectNotFound = fmt.Errorf("r2: object not found")

func contentDisposition(filename string) string {
	// RFC 6266: attachment; filename="…" with basic escaping.
	safe := strings.ReplaceAll(filename, `\`, `\\`)
	safe = strings.ReplaceAll(safe, `"`, `\"`)
	return `attachment; filename="` + safe + `"`
}
