package appstore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetTransactionSandboxFallback(t *testing.T) {
	var prodHits, sandHits atomic.Int32
	prodStatus := http.StatusUnauthorized
	sandStatus := http.StatusNotFound
	prod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prodHits.Add(1)
		w.WriteHeader(prodStatus)
	}))
	t.Cleanup(prod.Close)
	sand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandHits.Add(1)
		http.Error(w, `{"errorCode":4040010}`, sandStatus)
	}))
	t.Cleanup(sand.Close)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	c := NewClient(prod.Client(), nil)
	c.ProdURL = prod.URL
	c.Sandbox = sand.URL
	cfg := Config{IssuerID: "iss", KeyID: "kid", BundleID: "bid", PrivateKeyPEM: string(keyPEM)}

	_, err = c.GetTransaction(context.Background(), cfg, "tx", true)
	if prodHits.Load() != 1 || sandHits.Load() != 1 {
		t.Fatalf("allowed 401 should retry sandbox, prod=%d sand=%d", prodHits.Load(), sandHits.Load())
	}
	ae, ok := err.(*APIError)
	if !ok || ae.Status != http.StatusNotFound {
		t.Fatalf("want sandbox 404, got %v", err)
	}

	prodHits.Store(0)
	sandHits.Store(0)
	_, err = c.GetTransaction(context.Background(), cfg, "tx", false)
	if prodHits.Load() != 1 || sandHits.Load() != 0 {
		t.Fatalf("disallowed must not call sandbox, prod=%d sand=%d", prodHits.Load(), sandHits.Load())
	}
	ae, ok = err.(*APIError)
	if !ok || ae.Status != http.StatusUnauthorized {
		t.Fatalf("want production 401, got %v", err)
	}

	prodHits.Store(0)
	sandHits.Store(0)
	prodStatus = http.StatusInternalServerError
	_, err = c.GetTransaction(context.Background(), cfg, "tx", true)
	if prodHits.Load() != 1 || sandHits.Load() != 0 {
		t.Fatalf("500 must not retry sandbox, prod=%d sand=%d", prodHits.Load(), sandHits.Load())
	}
	if ae, ok = err.(*APIError); !ok || ae.Status != http.StatusInternalServerError {
		t.Fatalf("want production 500, got %v", err)
	}
}
