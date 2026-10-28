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

// MergeStripeAccountSettings sets account_id and validates livemode vs secret key prefix.
func MergeStripeAccountSettings(settings json.RawMessage, accountID, secretKey string) (json.RawMessage, bool, error) {
	var st map[string]any
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &st)
	}
	if st == nil {
		st = map[string]any{}
	}
	livemode, _ := st["livemode"].(bool)
	isLiveKey := strings.HasPrefix(secretKey, "sk_live_")
	isTestKey := strings.HasPrefix(secretKey, "sk_test_")
	if isLiveKey && !livemode {
		return nil, false, ErrValidationFailed.WithDetails(map[string]any{"reason": "livemode=false but secret key is live"})
	}
	if isTestKey && livemode {
		return nil, false, ErrValidationFailed.WithDetails(map[string]any{"reason": "livemode=true but secret key is test"})
	}
	if isLiveKey {
		livemode = true
		st["livemode"] = true
	}
	if isTestKey {
		livemode = false
		st["livemode"] = false
	}
	st["account_id"] = accountID
	b, err := json.Marshal(st)
	return b, livemode, err
}
