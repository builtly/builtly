// Package jwks fetches and caches JSON Web Key Sets for ID-token verification.
package jwks

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	minCacheTTL   = 5 * time.Minute
	maxRefetchGap = time.Minute
)

// Cache is a process-scoped JWKS cache keyed by URL.
type Cache struct {
	http *http.Client
	mu   sync.Mutex
	sets map[string]*cachedSet
}

type cachedSet struct {
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	lastFetch time.Time
}

// New constructs a Cache.
func New(hc *http.Client) *Cache {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Cache{http: hc, sets: map[string]*cachedSet{}}
}

// KeyFunc returns a jwt.Keyfunc that resolves RSA keys from url.
func (c *Cache) KeyFunc(url string) jwt.Keyfunc {
	return func(tok *jwt.Token) (any, error) {
		if tok.Method.Alg() != "RS256" {
			return nil, fmt.Errorf("jwks: unexpected alg %s", tok.Method.Alg())
		}
		kid, _ := tok.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("jwks: missing kid")
		}
		key, err := c.lookup(context.Background(), url, kid, false)
		if err != nil {
			return nil, err
		}
		return key, nil
	}
}

// VerifyRS256 parses and validates a JWT using JWKS at url.
func (c *Cache) VerifyRS256(ctx context.Context, url, token string, opts ...jwt.ParserOption) (*jwt.Token, error) {
	parserOpts := append([]jwt.ParserOption{jwt.WithValidMethods([]string{"RS256"})}, opts...)
	return jwt.Parse(token, func(tok *jwt.Token) (any, error) {
		kid, _ := tok.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("jwks: missing kid")
		}
		key, err := c.lookup(ctx, url, kid, false)
		if err != nil {
			// unknown kid → force refetch once
			key, err = c.lookup(ctx, url, kid, true)
			if err != nil {
				return nil, err
			}
		}
		return key, nil
	}, parserOpts...)
}

func (c *Cache) lookup(ctx context.Context, url, kid string, force bool) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	cs := c.sets[url]
	if cs != nil && !force && now.Before(cs.expires) {
		if k, ok := cs.keys[kid]; ok {
			return k, nil
		}
		force = true
	}
	if cs != nil && force && now.Sub(cs.lastFetch) < maxRefetchGap {
		if k, ok := cs.keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("jwks: unknown kid %q", kid)
	}
	keys, ttl, err := c.fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	if ttl < minCacheTTL {
		ttl = minCacheTTL
	}
	cs = &cachedSet{keys: keys, expires: now.Add(ttl), lastFetch: now}
	c.sets[url] = cs
	k, ok := cs.keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwks: unknown kid %q", kid)
	}
	return k, nil
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (c *Cache) fetch(ctx context.Context, url string) (map[string]*rsa.PublicKey, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("jwks: fetch %s: status %d", url, res.StatusCode)
	}
	var doc jwksDoc
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		return nil, 0, err
	}
	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		pub, err := rsaPublic(k.N, k.E)
		if err != nil {
			continue
		}
		out[k.Kid] = pub
	}
	ttl := parseCacheControl(res.Header.Get("Cache-Control"))
	return out, ttl, nil
}

func rsaPublic(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	var eInt int
	if len(eb) < 8 {
		buf := make([]byte, 8)
		copy(buf[8-len(eb):], eb)
		eInt = int(binary.BigEndian.Uint64(buf))
	} else {
		eInt = int(new(big.Int).SetBytes(eb).Int64())
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: eInt}, nil
}

func parseCacheControl(h string) time.Duration {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "max-age=") {
			sec, err := strconv.Atoi(strings.TrimPrefix(part, "max-age="))
			if err == nil && sec > 0 {
				return time.Duration(sec) * time.Second
			}
		}
	}
	return minCacheTTL
}

// SetKeysForTest injects keys for a URL (tests).
func (c *Cache) SetKeysForTest(url string, keys map[string]*rsa.PublicKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets[url] = &cachedSet{
		keys: keys, expires: time.Now().Add(time.Hour), lastFetch: time.Now(),
	}
}
