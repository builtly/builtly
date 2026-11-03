package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/builtly/builtly/internal/apperr"
)

func TestClassifyStripeSecret(t *testing.T) {
	cases := []struct {
		key     string
		live    bool
		known   bool
		wantErr bool
	}{
		{key: "sk_live_abc", live: true, known: true},
		{key: "sk_test_abc", live: false, known: true},
		{key: "rk_live_abc", live: true, known: true},
		{key: "rk_test_abc", live: false, known: true},
		{key: "  rk_live_abc  ", live: true, known: true},
		{key: "pk_live_abc", wantErr: true},
		{key: "pk_test_abc", wantErr: true},
		{key: "unknown_key", live: false, known: false},
	}
	for _, tc := range cases {
		live, known, err := ClassifyStripeSecret(tc.key)
		if tc.wantErr {
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Code != "validation_failed" {
				t.Fatalf("%s: want validation_failed, got %v", tc.key, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if live != tc.live || known != tc.known {
			t.Fatalf("%s: live=%v known=%v, want live=%v known=%v", tc.key, live, known, tc.live, tc.known)
		}
	}
}

func TestMergeStripeAccountSettingsRestrictedKey(t *testing.T) {
	merged, live, err := MergeStripeAccountSettings([]byte(`{"livemode":true}`), "acct_1", "rk_live_restricted")
	if err != nil {
		t.Fatal(err)
	}
	if !live || !strings.Contains(string(merged), `"livemode":true`) || !strings.Contains(string(merged), `"account_id":"acct_1"`) {
		t.Fatalf("merged=%s live=%v", merged, live)
	}

	_, _, err = MergeStripeAccountSettings([]byte(`{"livemode":false}`), "acct_1", "rk_live_restricted")
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Details["reason"] != "livemode=false but secret key is live" {
		t.Fatalf("mismatch: %v", err)
	}

	merged, live, err = MergeStripeAccountSettings([]byte(`{"livemode":true}`), "acct_1", "rk_test_restricted")
	if err == nil {
		t.Fatal("expected test key to reject livemode=true")
	}
	merged, live, err = MergeStripeAccountSettings([]byte(`{"livemode":false}`), "acct_1", "rk_test_restricted")
	if err != nil || live {
		t.Fatalf("test key: merged=%s live=%v err=%v", merged, live, err)
	}

	// Omitted livemode is inferred from key prefix (including live keys).
	merged, live, err = MergeStripeAccountSettings([]byte(`{}`), "acct_1", "rk_live_restricted")
	if err != nil || !live || !strings.Contains(string(merged), `"livemode":true`) {
		t.Fatalf("infer live: merged=%s live=%v err=%v", merged, live, err)
	}
	merged, live, err = MergeStripeAccountSettings(nil, "acct_1", "rk_test_restricted")
	if err != nil || live || !strings.Contains(string(merged), `"livemode":false`) {
		t.Fatalf("infer test: merged=%s live=%v err=%v", merged, live, err)
	}
}

func TestNormalizeSecretsJSONStripe(t *testing.T) {
	raw := []byte(`{"secret_key":"  rk_live_51AbCdEf  ","webhook_secrets":["  whsec_123  ",""]}`)
	if err := ValidateSecretsJSON(ProviderStripe, raw); err != nil {
		t.Fatalf("validate trimmed: %v", err)
	}
	norm, err := NormalizeSecretsJSON(ProviderStripe, raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var s StripeSecrets
	if err := json.Unmarshal(norm, &s); err != nil {
		t.Fatal(err)
	}
	if s.SecretKey != "rk_live_51AbCdEf" || len(s.WebhookSecrets) != 1 || s.WebhookSecrets[0] != "whsec_123" {
		t.Fatalf("unexpected normalized secrets: %+v", s)
	}
}
