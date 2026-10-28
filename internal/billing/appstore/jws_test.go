package appstore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyJWSWrongRoot(t *testing.T) {
	chain := mustGenChain(t, false)
	otherRoot := genRoot(t)
	v, err := NewVerifierWithRootPEM(otherRoot.pem)
	if err != nil {
		t.Fatal(err)
	}
	tok := chain.sign(t, map[string]any{"transactionId": "t1"})
	if _, err := v.VerifyJWS(tok); err == nil {
		t.Fatal("want chain error for wrong root")
	}
}

func TestVerifyJWSWrongOID(t *testing.T) {
	chain := mustGenChain(t, false) // no OIDs
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(chain.rootPEM)
	v := &Verifier{roots: pool, requireOID: true, now: time.Now}
	tok := chain.sign(t, map[string]any{"transactionId": "t1"})
	if _, err := v.VerifyJWS(tok); err == nil {
		t.Fatal("want OID error")
	}
}

func TestVerifyJWSOK(t *testing.T) {
	chain := mustGenChain(t, true)
	v, err := NewVerifierWithRootPEM(chain.rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	// force OID check with test root
	v.requireOID = true
	payload := map[string]any{"transactionId": "tx-ok", "productId": "credits_100"}
	tok := chain.sign(t, payload)
	raw, err := v.VerifyJWS(tok)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["transactionId"] != "tx-ok" {
		t.Fatalf("payload: %v", got)
	}
}

type testChain struct {
	rootPEM  []byte
	leafKey  *ecdsa.PrivateKey
	leafDER  []byte
	interDER []byte
}

type rootBundle struct {
	pem []byte
	key *ecdsa.PrivateKey
	crt *x509.Certificate
}

func genRoot(t *testing.T) rootBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return rootBundle{pem: pemBytes, key: key, crt: crt}
}

func mustGenChain(t *testing.T, withOID bool) testChain {
	t.Helper()
	root := genRoot(t)
	rootKey := root.key

	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test Intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	if withOID {
		interTmpl.ExtraExtensions = []pkix.Extension{{
			Id: oidIntermediate, Value: mustMarshalNULL(t),
		}}
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, root.crt, &interKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	interCrt, err := x509.ParseCertificate(interDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "Test Leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if withOID {
		leafTmpl.ExtraExtensions = []pkix.Extension{{
			Id: oidLeaf, Value: mustMarshalNULL(t),
		}}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, interCrt, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatal(err)
	}
	return testChain{rootPEM: root.pem, leafKey: leafKey, leafDER: leafDER, interDER: interDER}
}

func mustMarshalNULL(t *testing.T) []byte {
	t.Helper()
	b, err := asn1.Marshal(asn1.RawValue{Tag: 5})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (c testChain) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	tok.Header["x5c"] = []string{
		base64.StdEncoding.EncodeToString(c.leafDER),
		base64.StdEncoding.EncodeToString(c.interDER),
	}
	s, err := tok.SignedString(c.leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
