package integration

import (
	"context"
	"encoding/json"
	"strings"
)

// StripeAccountSettings are non-secret Stripe connection settings.
type StripeAccountSettings struct {
	LiveMode  bool   `json:"livemode"`
	AccountID string `json:"account_id"`
}

// FindStripeAccountDuplicate returns an existing connection id with the same account_id+livemode, or "".
func (s *Service) FindStripeAccountDuplicate(ctx context.Context, accountID string, livemode bool, excludeID string) (string, error) {
	rows, err := s.store.ListConnections(ctx, nil, ProviderStripe)
	if err != nil {
		return "", err
	}
	for _, c := range rows {
		if excludeID != "" && c.ID == excludeID {
			continue
		}
		var st StripeAccountSettings
		_ = json.Unmarshal(c.Settings, &st)
		if st.AccountID == accountID && st.LiveMode == livemode {
			return c.ID, nil
		}
	}
	return "", nil
}

// ClassifyStripeSecret reports whether a secret or restricted key is live.
// Publishable keys (pk_) cannot call the API and are rejected.
// Unknown prefixes are allowed; known is then false and livemode is not inferred.
func ClassifyStripeSecret(secretKey string) (live, known bool, err error) {
	key := strings.TrimSpace(secretKey)
	switch {
	case strings.HasPrefix(key, "pk_"):
		return false, false, ErrValidationFailed.WithDetails(map[string]any{
			"reason": "publishable key (pk_) cannot be used as the secret key; paste a restricted key (rk_) or secret key (sk_)",
		})
	case strings.HasPrefix(key, "sk_live_"), strings.HasPrefix(key, "rk_live_"):
		return true, true, nil
	case strings.HasPrefix(key, "sk_test_"), strings.HasPrefix(key, "rk_test_"):
		return false, true, nil
	default:
		return false, false, nil
	}
}

// MergeStripeAccountSettings sets account_id and validates livemode vs secret key prefix.
func MergeStripeAccountSettings(settings json.RawMessage, accountID, secretKey string) (json.RawMessage, bool, error) {
	var st map[string]any
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &st)
	}
	if st == nil {
		st = map[string]any{}
	}
	livemode, hasLivemode := st["livemode"].(bool)
	live, known, err := ClassifyStripeSecret(secretKey)
	if err != nil {
		return nil, false, err
	}
	if known && hasLivemode && live && !livemode {
		return nil, false, ErrValidationFailed.WithDetails(map[string]any{"reason": "livemode=false but secret key is live"})
	}
	if known && hasLivemode && !live && livemode {
		return nil, false, ErrValidationFailed.WithDetails(map[string]any{"reason": "livemode=true but secret key is test"})
	}
	if known {
		livemode = live
		st["livemode"] = live
	}
	st["account_id"] = accountID
	b, err := json.Marshal(st)
	return b, livemode, err
}
