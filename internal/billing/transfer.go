package billing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/builtly/builtly/internal/credit"
	"github.com/builtly/builtly/internal/platform/db"
	"github.com/builtly/builtly/internal/tenant"
)

// TransferGuestCreditsInput is the body for POST /billing/transfer-guest-credits.
type TransferGuestCreditsInput struct {
	GuestAccessToken  string `json:"guest_access_token"`
	GuestRefreshToken string `json:"guest_refresh_token"`
}

// TransferGuestCreditsResult is the response payload.
type TransferGuestCreditsResult struct {
	TransferredCredits int64 `json:"transferred_credits"`
	Balance            int64 `json:"balance"`
	GuestBalance       int64 `json:"guest_balance"`
	AlreadyTransferred bool  `json:"already_transferred"`
}

// TransferGuestCredits moves remaining credits and purchases from a guest account to a formal user account.
func (s *Service) TransferGuestCredits(ctx context.Context, app tenant.App, formalUserID uuid.UUID, in TransferGuestCreditsInput) (TransferGuestCreditsResult, error) {
	if !app.Settings.Auth.GuestLoginEnabled || app.Settings.Auth.GuestEmailDomain == "" {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "guest_email_domain_not_configured",
		})
	}
	if in.GuestAccessToken == "" && in.GuestRefreshToken == "" {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "guest_token_required",
		})
	}

	guestUserID, err := s.resolveGuestUserID(ctx, app.ID, in)
	if err != nil {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": err.Error(),
		})
	}
	if guestUserID == formalUserID {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "cannot_transfer_to_self",
		})
	}

	// Load users to verify roles & status
	guestUser, err := s.users.GetByID(ctx, nil, app.ID, guestUserID)
	if err != nil {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "guest_user_not_found",
		})
	}
	if guestUser.Email == nil || !app.Settings.IsGuestEmail(*guestUser.Email) {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "account_is_not_guest",
		})
	}
	// Verify guest user has no linked external OAuth identities (must be pure guest)
	identities, err := s.users.ListIdentities(ctx, nil, app.ID, guestUserID)
	if err != nil {
		return TransferGuestCreditsResult{}, err
	}
	if len(identities) > 0 {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "guest_account_has_identities",
		})
	}

	formalUser, err := s.users.GetByID(ctx, nil, app.ID, formalUserID)
	if err != nil {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "formal_user_not_found",
		})
	}
	if formalUser.Email != nil && app.Settings.IsGuestEmail(*formalUser.Email) {
		return TransferGuestCreditsResult{}, ErrGuestTransferFailed.WithDetails(map[string]any{
			"reason": "target_user_cannot_be_guest",
		})
	}

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var res TransferGuestCreditsResult
		var retry bool
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			now := db.Now()

			// Check previous transfers for this guest to avoid stealing across different formal accounts
			priorTransfer, err := s.credits.GetByReferenceTx(ctx, tx, app.ID, credit.TypeAdminAdjust, "guest_transfer_out", guestUserID.String())
			if err == nil {
				// Prior transfer exists; check if target is the same formal user
				var meta map[string]any
				_ = json.Unmarshal(priorTransfer.Metadata, &meta)
				targetID, _ := meta["to_user_id"].(string)
				if targetID != formalUserID.String() {
					return ErrGuestAlreadyTransferred
				}
			} else if !errors.Is(err, db.ErrNotFound) {
				return err
			}

			// Reassign purchases in this transaction (re-entrant/idempotent)
			if _, err := s.store.ReassignPurchasesUser(ctx, tx, app.ID, guestUserID, formalUserID, now); err != nil {
				return err
			}

			// Check guest current balance
			guestBal, err := s.credits.GetBalanceTx(ctx, tx, app.ID, guestUserID)
			if err != nil {
				return err
			}
			formalBal, err := s.credits.GetBalanceTx(ctx, tx, app.ID, formalUserID)
			if err != nil {
				return err
			}

			if guestBal.Balance <= 0 {
				// Idempotent branch: guest has 0 balance left
				res = TransferGuestCreditsResult{
					TransferredCredits: 0,
					Balance:            formalBal.Balance,
					GuestBalance:       0,
					AlreadyTransferred: priorTransfer.ID != uuid.Nil,
				}
				return nil
			}

			creditsToTransfer := guestBal.Balance
			// Deduct guest balance atomically via CAS
			ok, err := s.credits.Store().CASZeroBalance(ctx, tx, app.ID, guestUserID, creditsToTransfer, now)
			if err != nil {
				return err
			}
			if !ok {
				retry = true
				return errors.New("concurrent balance modification")
			}

			// Add to formal user
			newFormalBal, err := s.credits.Store().ApplyBalanceDelta(ctx, tx, app.ID, formalUserID, creditsToTransfer, now)
			if err != nil {
				return err
			}

			transferID := uuid.Must(uuid.NewV7()).String()

			// Record Guest debit ledger
			refOutType := "guest_transfer_out"
			refOutID := guestUserID.String()
			actorFormal := "user:" + formalUserID.String()
			metaOut, _ := json.Marshal(map[string]any{
				"to_user_id":  formalUserID.String(),
				"transfer_id": transferID,
			})
			outEntry := credit.Entry{
				ID:             uuid.Must(uuid.NewV7()),
				AppID:          app.ID,
				UserID:         guestUserID,
				EntryType:      credit.TypeAdminAdjust,
				Amount:         -creditsToTransfer,
				BalanceAfter:   0,
				IdempotencyKey: "guest_transfer:out:" + guestUserID.String() + ":" + transferID,
				ReferenceType:  &refOutType,
				ReferenceID:    &refOutID,
				Actor:          actorFormal,
				Reason:         "guest_credits_transfer_out",
				Metadata:       metaOut,
				CreatedAt:      now,
			}
			if err := s.credits.Store().InsertEntry(ctx, tx, outEntry); err != nil {
				return err
			}

			// Record Formal user credit ledger
			refInType := "guest_transfer_in"
			refInID := guestUserID.String()
			metaIn, _ := json.Marshal(map[string]any{
				"from_user_id": guestUserID.String(),
				"transfer_id":  transferID,
			})
			inEntry := credit.Entry{
				ID:             uuid.Must(uuid.NewV7()),
				AppID:          app.ID,
				UserID:         formalUserID,
				EntryType:      credit.TypeAdminAdjust,
				Amount:         creditsToTransfer,
				BalanceAfter:   newFormalBal,
				IdempotencyKey: "guest_transfer:in:" + formalUserID.String() + ":" + transferID,
				ReferenceType:  &refInType,
				ReferenceID:    &refInID,
				Actor:          actorFormal,
				Reason:         "guest_credits_transfer_in",
				Metadata:       metaIn,
				CreatedAt:      now,
			}
			if err := s.credits.Store().InsertEntry(ctx, tx, inEntry); err != nil {
				return err
			}

			res = TransferGuestCreditsResult{
				TransferredCredits: creditsToTransfer,
				Balance:            newFormalBal,
				GuestBalance:       0,
				AlreadyTransferred: false,
			}
			return nil
		})

		if retry {
			continue
		}
		if err != nil {
			return TransferGuestCreditsResult{}, err
		}
		return res, nil
	}

	return TransferGuestCreditsResult{}, credit.ErrConcurrentMod
}

func (s *Service) resolveGuestUserID(ctx context.Context, appID uuid.UUID, in TransferGuestCreditsInput) (uuid.UUID, error) {
	if in.GuestAccessToken != "" && s.authTokens != nil {
		info, err := s.authTokens.VerifyAccessIgnoreExpiry(in.GuestAccessToken, appID)
		if err == nil && info.UserID != uuid.Nil {
			return info.UserID, nil
		}
	}

	if in.GuestRefreshToken != "" {
		raw := strings.TrimSpace(in.GuestRefreshToken)
		sum := sha256.Sum256([]byte(raw))
		rt, err := s.users.GetRefreshByHash(ctx, nil, sum[:])
		if err == nil && rt.AppID == appID && rt.UserID != uuid.Nil {
			return rt.UserID, nil
		}
	}

	return uuid.Nil, errors.New("invalid_guest_token")
}
