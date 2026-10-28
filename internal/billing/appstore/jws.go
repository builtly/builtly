package appstore

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "embed"

	"github.com/golang-jwt/jwt/v5"
)

//go:embed AppleRootCA-G3.pem
var appleRootCAPEM []byte

// OIDs required by App Store Server API JWS chain (§3.6).
var (
	oidLeaf         = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
	oidIntermediate = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}
)

// Verifier verifies App Store JWS (transactions + notifications).
type Verifier struct {
	roots      *x509.CertPool
	requireOID bool
	now        func() time.Time
}

// NewVerifier constructs a Verifier trusting the embedded Apple Root CA – G3.
func NewVerifier() (*Verifier, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(appleRootCAPEM) {
		return nil, fmt.Errorf("appstore: load Apple Root CA G3")
	}
	return &Verifier{roots: pool, requireOID: true, now: time.Now}, nil
}

// NewVerifierWithRootPEM is for tests (inject generated CA; OID checks skipped).
func NewVerifierWithRootPEM(pemBytes []byte) (*Verifier, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("appstore: invalid root PEM")
	}
	return &Verifier{roots: pool, requireOID: false, now: time.Now}, nil
}

// SetNow overrides the clock (tests).
func (v *Verifier) SetNow(fn func() time.Time) { v.now = fn }

// VerifyJWS parses and verifies a compact JWS, returning the payload bytes.
func (v *Verifier) VerifyJWS(token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("appstore: malformed jws")
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct {
		Alg string   `json:"alg"`
		X5c []string `json:"x5c"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		return nil, err
	}
	if hdr.Alg != "ES256" {
		return nil, fmt.Errorf("appstore: unexpected alg %s", hdr.Alg)
	}
	if len(hdr.X5c) < 2 {
		return nil, fmt.Errorf("appstore: x5c too short")
	}
	certs := make([]*x509.Certificate, 0, len(hdr.X5c))
	for _, b64 := range hdr.X5c {
		der, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("appstore: x5c decode: %w", err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	leaf, intermediate := certs[0], certs[1]
	if err := v.verifyChain(leaf, intermediate); err != nil {
		return nil, err
	}
	if v.requireOID {
		if !hasOID(leaf, oidLeaf) {
			return nil, fmt.Errorf("appstore: leaf missing required OID")
		}
		if !hasOID(intermediate, oidIntermediate) {
			return nil, fmt.Errorf("appstore: intermediate missing required OID")
		}
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("appstore: leaf not ECDSA")
	}
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "ES256" {
			return nil, fmt.Errorf("unexpected alg")
		}
		return pub, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, fmt.Errorf("appstore: invalid jws")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (v *Verifier) verifyChain(leaf, intermediate *x509.Certificate) error {
	opts := x509.VerifyOptions{
		Roots:         v.roots,
		Intermediates: x509.NewCertPool(),
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	opts.Intermediates.AddCert(intermediate)
	if _, err := leaf.Verify(opts); err != nil {
		return fmt.Errorf("appstore: chain: %w", err)
	}
	return nil
}

func hasOID(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, e := range cert.Extensions {
		if e.Id.Equal(oid) {
			return true
		}
	}
	for _, e := range cert.ExtraExtensions {
		if e.Id.Equal(oid) {
			return true
		}
	}
	return false
}

// ParseRootPEM loads the first certificate from PEM (tests).
func ParseRootPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no pem")
	}
	return x509.ParseCertificate(block.Bytes)
}
