// Package s3sig implements AWS Signature Version 4 for S3-compatible APIs (R2).
package s3sig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm = "AWS4-HMAC-SHA256"
	emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// Credentials are static access keys.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // optional
}

// SignInput describes a request to sign with the Authorization header.
type SignInput struct {
	Method      string
	URL         *url.URL
	Headers     http.Header
	BodySHA256  string // hex; empty → empty payload hash
	Service     string
	Region      string
	Credentials Credentials
	Now         time.Time
}

// SignResult holds Authorization and companion headers.
type SignResult struct {
	Authorization string
	AmzDate       string
	ContentSHA256 string
	SignedHeaders string
}

// Sign mutates Headers with X-Amz-Date, X-Amz-Content-Sha256, and Authorization (S3-style).
func Sign(in SignInput) SignResult {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	amzDate := in.Now.UTC().Format("20060102T150405Z")
	payloadHash := in.BodySHA256
	if payloadHash == "" {
		payloadHash = emptyHash
	}
	if in.Headers == nil {
		in.Headers = make(http.Header)
	}
	in.Headers.Set("Host", hostHeader(in.URL))
	in.Headers.Set("X-Amz-Date", amzDate)
	in.Headers.Set("X-Amz-Content-Sha256", payloadHash)
	if in.Credentials.SessionToken != "" {
		in.Headers.Set("X-Amz-Security-Token", in.Credentials.SessionToken)
	}

	signedHeaders, canonHdrs := canonicalHeaders(in.Headers, true)
	auth := authorization(AuthParams{
		Method: in.Method, Path: in.URL.Path, Query: in.URL.Query(),
		CanonicalHeaders: canonHdrs, SignedHeaders: signedHeaders,
		PayloadHash: payloadHash, AmzDate: amzDate,
		Region: in.Region, Service: in.Service, Creds: in.Credentials,
	})
	in.Headers.Set("Authorization", auth)
	return SignResult{
		Authorization: auth, AmzDate: amzDate,
		ContentSHA256: payloadHash, SignedHeaders: signedHeaders,
	}
}

// PresignInput describes a query-string authenticated request.
type PresignInput struct {
	Method           string
	URL              *url.URL
	Headers          http.Header
	SignedHeaderKeys []string // lower-case; default ["host"]
	Expires          time.Duration
	Service          string
	Region           string
	Credentials      Credentials
	Now              time.Time
	PayloadHash      string // default UNSIGNED-PAYLOAD
}

// Presign returns a URL with SigV4 query authentication.
func Presign(in PresignInput) *url.URL {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if in.Expires <= 0 {
		in.Expires = 15 * time.Minute
	}
	amzDate := in.Now.UTC().Format("20060102T150405Z")
	dateStamp := in.Now.UTC().Format("20060102")
	payloadHash := in.PayloadHash
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}
	h := cloneHeader(in.Headers)
	h.Set("Host", hostHeader(in.URL))
	keys := in.SignedHeaderKeys
	if len(keys) == 0 {
		keys = []string{"host"}
	}
	signedHeaders, canonHdrs := canonicalHeadersForKeys(h, keys)

	scope := strings.Join([]string{dateStamp, in.Region, in.Service, "aws4_request"}, "/")
	q := cloneValues(in.URL.Query())
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", in.Credentials.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(in.Expires.Seconds())))
	q.Set("X-Amz-SignedHeaders", signedHeaders)
	if in.Credentials.SessionToken != "" {
		q.Set("X-Amz-Security-Token", in.Credentials.SessionToken)
	}

	stringToSign := buildStringToSign(in.Method, in.URL.Path, q, canonHdrs, signedHeaders, payloadHash, amzDate, scope)
	sig := hex.EncodeToString(hmacSHA256(deriveKey(in.Credentials.SecretAccessKey, dateStamp, in.Region, in.Service), []byte(stringToSign)))
	q.Set("X-Amz-Signature", sig)

	out := *in.URL
	out.RawQuery = canonicalQueryString(q)
	return &out
}

// AuthParams are low-level inputs for Authorization (used by tests and Sign).
type AuthParams struct {
	Method           string
	Path             string
	Query            url.Values
	CanonicalHeaders string // including trailing newlines per header
	SignedHeaders    string
	PayloadHash      string
	AmzDate          string // 20060102T150405Z
	Region           string
	Service          string
	Creds            Credentials
}

// Authorization builds the Authorization header value.
func authorization(p AuthParams) string {
	dateStamp := p.AmzDate[:8]
	scope := strings.Join([]string{dateStamp, p.Region, p.Service, "aws4_request"}, "/")
	sts := buildStringToSign(p.Method, p.Path, p.Query, p.CanonicalHeaders, p.SignedHeaders, p.PayloadHash, p.AmzDate, scope)
	sig := hex.EncodeToString(hmacSHA256(deriveKey(p.Creds.SecretAccessKey, dateStamp, p.Region, p.Service), []byte(sts)))
	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, p.Creds.AccessKeyID, scope, p.SignedHeaders, sig)
}

// SignTestSuite signs like the AWS SigV4 test suite (no forced content-sha256 header).
func SignTestSuite(method, path string, query url.Values, headers http.Header, amzDate, region, service string, creds Credentials) string {
	headers = cloneHeader(headers)
	headers.Set("X-Amz-Date", amzDate)
	signedHeaders, canonHdrs := canonicalHeaders(headers, false)
	return authorization(AuthParams{
		Method: method, Path: path, Query: query,
		CanonicalHeaders: canonHdrs, SignedHeaders: signedHeaders,
		PayloadHash: emptyHash, AmzDate: amzDate,
		Region: region, Service: service, Creds: creds,
	})
}

func buildStringToSign(method, path string, query url.Values, canonHdrs, signedHeaders, payloadHash, amzDate, scope string) string {
	canonicalReq := strings.Join([]string{
		strings.ToUpper(method),
		canonicalURI(path),
		canonicalQueryString(query),
		canonHdrs,
		signedHeaders,
		payloadHash,
	}, "\n")
	return strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hashHex([]byte(canonicalReq)),
	}, "\n")
}

func deriveKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(data)
	return m.Sum(nil)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hostHeader(u *url.URL) string {
	return u.Host
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = uriEncode(p, false)
	}
	out := strings.Join(parts, "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

func canonicalQueryString(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	first := true
	for _, k := range keys {
		vals := append([]string{}, q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			if !first {
				b.WriteByte('&')
			}
			first = false
			b.WriteString(uriEncode(k, true))
			b.WriteByte('=')
			b.WriteString(uriEncode(v, true))
		}
	}
	return b.String()
}

func canonicalHeaders(h http.Header, skipAuth bool) (signedHeaders, canonical string) {
	keys := make([]string, 0, len(h))
	seen := map[string]bool{}
	for k := range h {
		lk := strings.ToLower(k)
		if skipAuth && lk == "authorization" {
			continue
		}
		if seen[lk] {
			continue
		}
		seen[lk] = true
		keys = append(keys, lk)
	}
	sort.Strings(keys)
	return formatCanonicalHeaders(h, keys)
}

func canonicalHeadersForKeys(h http.Header, keys []string) (string, string) {
	norm := make([]string, len(keys))
	copy(norm, keys)
	for i := range norm {
		norm[i] = strings.ToLower(norm[i])
	}
	sort.Strings(norm)
	return formatCanonicalHeaders(h, norm)
}

func formatCanonicalHeaders(h http.Header, keys []string) (string, string) {
	var b strings.Builder
	for _, k := range keys {
		vals := h.Values(http.CanonicalHeaderKey(k))
		if len(vals) == 0 {
			vals = h.Values(k)
		}
		joined := strings.TrimSpace(strings.Join(vals, ","))
		joined = strings.Join(strings.Fields(joined), " ")
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(joined)
		b.WriteByte('\n')
	}
	return strings.Join(keys, ";"), b.String()
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		out[k] = append([]string{}, vs...)
	}
	return out
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string{}, vs...)
	}
	return out
}

func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (!encodeSlash && c == '/') {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// HashPayload returns the hex SHA-256 of body.
func HashPayload(body []byte) string {
	if len(body) == 0 {
		return emptyHash
	}
	return hashHex(body)
}

// EmptyPayloadHash is the SHA-256 of an empty body.
func EmptyPayloadHash() string { return emptyHash }

// DeriveSigningKeyHex exposes key derivation for AWS doc vectors.
func DeriveSigningKeyHex(secret, dateStamp, region, service string) string {
	return hex.EncodeToString(deriveKey(secret, dateStamp, region, service))
}
