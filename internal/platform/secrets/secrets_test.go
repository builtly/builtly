package secrets_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/builtly/builtly/internal/config"
	"github.com/builtly/builtly/internal/platform/secrets"
)

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	b, err := secrets.NewBox([]config.KeyMaterial{{
		ID:  "1",
		Key: key,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenAAD(t *testing.T) {
	box := testBox(t)
	aad := secrets.ConnectionAAD("conn-1", "stripe")
	blob, ver, err := box.Seal([]byte(`{"secret_key":"sk_live_abc"}`), aad)
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("version %d", ver)
	}
	plain, err := box.Open(blob, ver, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"secret_key":"sk_live_abc"}` {
		t.Fatalf("plain %s", plain)
	}
	_, err = box.Open(blob, ver, secrets.ConnectionAAD("conn-2", "stripe"))
	if err == nil {
		t.Fatal("expected AAD mismatch to fail")
	}
}

func TestMaskSecretNeverFull(t *testing.T) {
	raw := "sk_live_51AbCdEfGhIjKlMnOpQr"
	masked := secrets.MaskSecret(raw)
	if masked == raw {
		t.Fatal("mask equalled raw secret")
	}
	if strings.Contains(masked, "EfGhIjKl") {
		t.Fatalf("mask leaked middle: %s", masked)
	}
	if !strings.HasSuffix(masked, raw[len(raw)-4:]) {
		t.Fatalf("expected last4 preserved: %s", masked)
	}
	rk := secrets.MaskSecret("  rk_live_51AbCdEfGhIjKlMnOpQr  ")
	if !strings.HasPrefix(rk, "rk_live_") || !strings.HasSuffix(rk, "OpQr") {
		t.Fatalf("restricted key prefix/suffix not kept: %s", rk)
	}
}

func TestAPIKeyFormat(t *testing.T) {
	raw, hash, prefix, err := secrets.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "bk_") {
		t.Fatalf("prefix: %s", raw)
	}
	if len(prefix) != 12 {
		t.Fatalf("display prefix len %d", len(prefix))
	}
	if !bytes.Equal(hash, secrets.HashSHA256([]byte(raw))) {
		t.Fatal("hash mismatch")
	}
	// ensure key material length
	b64 := strings.TrimPrefix(raw, "bk_")
	dec, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil || len(dec) != 32 {
		t.Fatalf("key bytes: %v %d", err, len(dec))
	}
}
