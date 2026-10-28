package stripeapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// VerifySignature validates Stripe-Signature header against body and secrets.
// Returns nil if any secret matches a v1 signature within ±300s of t.
func VerifySignature(header string, body []byte, secrets []string, now time.Time) error {
	if header == "" || len(secrets) == 0 {
		return fmt.Errorf("stripe: missing signature or secrets")
	}
	var ts int64
	var v1s []string
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("stripe: bad timestamp")
			}
			ts = n
		case "v1":
			v1s = append(v1s, v)
		}
	}
	if ts == 0 || len(v1s) == 0 {
		return fmt.Errorf("stripe: incomplete signature")
	}
	if d := now.Unix() - ts; d > 300 || d < -300 {
		return fmt.Errorf("stripe: timestamp outside tolerance")
	}
	payload := strconv.FormatInt(ts, 10) + "." + string(body)
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(payload))
		want := hex.EncodeToString(mac.Sum(nil))
		for _, got := range v1s {
			if hmac.Equal([]byte(want), []byte(got)) {
				return nil
			}
		}
	}
	return fmt.Errorf("stripe: signature mismatch")
}
