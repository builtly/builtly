package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/builtly/builtly/internal/auth"
	"github.com/builtly/builtly/internal/billing"
	"github.com/builtly/builtly/internal/catalog"
	"github.com/builtly/builtly/internal/tenant"
)

func TestTransferGuestCredits(t *testing.T) {
	h := setupIAP(t)

	// Create app with guest email domain and signup bonus
	app, err := h.tSvc.Create(context.Background(), "guestapp", "GuestApp", tenant.Settings{
		AllowedOrigins:     []string{"https://guestapp.com"},
		AllowIAPSandbox:    true,
		SignupBonusCredits: 50,
		Auth: tenant.AuthSettings{
			PasswordEnabled:  true,
			GuestEmailDomain: "device.example.com",
		},
		Email: tenant.EmailSettings{FromAddress: "n@guestapp.com", ActionBaseURL: "https://guestapp.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)

	appleID := "credits_200"
	_, err = h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack200", Name: "200", Credits: 200, PriceAmount: 999, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Register Guest Account: guest_12345@device.example.com
	guestEmail := "guest_12345@device.example.com"
	guestReg, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: guestEmail, Password: "guestpassword123",
	})
	if err != nil {
		t.Fatalf("register guest: %v", err)
	}
	// Verify rule: Guest receives NO signup bonus!
	guestBal, err := h.credits.GetBalance(context.Background(), app.ID, guestReg.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if guestBal.Balance != 0 {
		t.Fatalf("expected guest balance 0, got %d", guestBal.Balance)
	}

	// 2. Guest performs IAP verify-iap for 200 credits
	txID := "tx_guest_iap_001"
	h.putAppleTx(t, txID, appleID, "Sandbox", guestReg.User.ID)
	rrIAP := h.verifyApple(t, guestReg.Tokens.AccessToken, app.ID, txID)
	if rrIAP.Code != 200 {
		t.Fatalf("guest verify-iap: %d %s", rrIAP.Code, rrIAP.Body.String())
	}
	guestBal, _ = h.credits.GetBalance(context.Background(), app.ID, guestReg.User.ID)
	if guestBal.Balance != 200 {
		t.Fatalf("expected guest balance 200 after IAP, got %d", guestBal.Balance)
	}

	// 3. Register Formal User (receives signup bonus = 50)
	formalEmail := "realuser@example.com"
	formalReg, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: formalEmail, Password: "realpassword123",
	})
	if err != nil {
		t.Fatalf("register formal user: %v", err)
	}
	formalBal, _ := h.credits.GetBalance(context.Background(), app.ID, formalReg.User.ID)
	if formalBal.Balance != 50 {
		t.Fatalf("expected formal user balance 50, got %d", formalBal.Balance)
	}

	// 4. Verification that another user CANNOT verify the same IAP transaction (prevent double-spend)
	rrDoubleVerify := h.verifyApple(t, formalReg.Tokens.AccessToken, app.ID, txID)
	if rrDoubleVerify.Code != 409 {
		t.Fatalf("expected 409 when formal user tries to verify guest's IAP, got %d %s", rrDoubleVerify.Code, rrDoubleVerify.Body.String())
	}

	// 5. Transfer Guest Credits to Formal User using guest_access_token
	transferBody, _ := json.Marshal(map[string]any{
		"guest_access_token": guestReg.Tokens.AccessToken,
	})
	req := httptest.NewRequest("POST", "/api/v1/billing/transfer-guest-credits", bytes.NewReader(transferBody))
	req.Header.Set("Authorization", "Bearer "+formalReg.Tokens.AccessToken)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("transfer guest credits: %d %s", rr.Code, rr.Body.String())
	}

	var res struct {
		TransferredCredits int64 `json:"transferred_credits"`
		Balance            int64 `json:"balance"`
		GuestBalance       int64 `json:"guest_balance"`
		AlreadyTransferred bool  `json:"already_transferred"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.TransferredCredits != 200 {
		t.Errorf("expected 200 transferred credits, got %d", res.TransferredCredits)
	}
	if res.Balance != 250 {
		t.Errorf("expected formal balance 250 (50 bonus + 200 transferred), got %d", res.Balance)
	}
	if res.GuestBalance != 0 {
		t.Errorf("expected guest balance 0, got %d", res.GuestBalance)
	}
	if res.AlreadyTransferred {
		t.Errorf("expected already_transferred false on first transfer")
	}

	// Confirm DB balance
	guestBal, _ = h.credits.GetBalance(context.Background(), app.ID, guestReg.User.ID)
	if guestBal.Balance != 0 {
		t.Errorf("DB guest balance should be 0, got %d", guestBal.Balance)
	}
	formalBal, _ = h.credits.GetBalance(context.Background(), app.ID, formalReg.User.ID)
	if formalBal.Balance != 250 {
		t.Errorf("DB formal balance should be 250, got %d", formalBal.Balance)
	}

	// 6. Idempotency test: Call transfer again from the SAME formal user
	req2 := httptest.NewRequest("POST", "/api/v1/billing/transfer-guest-credits", bytes.NewReader(transferBody))
	req2.Header.Set("Authorization", "Bearer "+formalReg.Tokens.AccessToken)
	req2.Header.Set("X-App-ID", app.ID.String())
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("idempotent transfer: %d %s", rr2.Code, rr2.Body.String())
	}
	var res2 struct {
		TransferredCredits int64 `json:"transferred_credits"`
		Balance            int64 `json:"balance"`
		GuestBalance       int64 `json:"guest_balance"`
		AlreadyTransferred bool  `json:"already_transferred"`
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &res2)
	if res2.TransferredCredits != 0 {
		t.Errorf("idempotent transfer should transfer 0 credits, got %d", res2.TransferredCredits)
	}
	if res2.Balance != 250 {
		t.Errorf("idempotent transfer balance should remain 250, got %d", res2.Balance)
	}
	if !res2.AlreadyTransferred {
		t.Errorf("expected already_transferred true on repeat transfer")
	}

	// 7. Double-claim prevention: Another formal user (User Z) attempts to transfer the same guest
	userZReg, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: "userz@example.com", Password: "password123",
	})
	if err != nil {
		t.Fatal(err)
	}
	req3 := httptest.NewRequest("POST", "/api/v1/billing/transfer-guest-credits", bytes.NewReader(transferBody))
	req3.Header.Set("Authorization", "Bearer "+userZReg.Tokens.AccessToken)
	req3.Header.Set("X-App-ID", app.ID.String())
	req3.Header.Set("Content-Type", "application/json")
	rr3 := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr3, req3)
	if rr3.Code != 409 {
		t.Fatalf("expected 409 conflict when another user claims already-transferred guest, got %d %s", rr3.Code, rr3.Body.String())
	}

	// 8. Reconcile ledger integrity check
	mismatches, err := h.credits.Reconcile(context.Background(), &app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatches) > 0 {
		t.Fatalf("reconcile ledger failed: %+v", mismatches)
	}
}

func TestTransferGuestCreditsWithRefreshTokenAndPurchaseReassignment(t *testing.T) {
	h := setupIAP(t)

	app, err := h.tSvc.Create(context.Background(), "rtapp", "RTApp", tenant.Settings{
		AllowedOrigins:  []string{"https://rtapp.com"},
		AllowIAPSandbox: true,
		Auth: tenant.AuthSettings{
			PasswordEnabled:  true,
			GuestEmailDomain: "device.example.com",
		},
		Email: tenant.EmailSettings{FromAddress: "n@rtapp.com", ActionBaseURL: "https://rtapp.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	conn := h.createAppleConn(t)
	h.bind(t, app.ID.String(), "apple_iap", conn)

	appleID := "credits_300"
	_, err = h.catalog.Create(context.Background(), app.ID, catalog.CreateInput{
		SKU: "pack300", Name: "300", Credits: 300, PriceAmount: 1999, Currency: "usd",
		AppleProductID: &appleID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Guest purchase
	guestReg, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: "guest_67890@device.example.com", Password: "guestpassword123",
	})
	if err != nil {
		t.Fatal(err)
	}
	txID := "tx_rt_reassign_1"
	h.putAppleTx(t, txID, appleID, "Sandbox", guestReg.User.ID)
	rrIAP := h.verifyApple(t, guestReg.Tokens.AccessToken, app.ID, txID)
	if rrIAP.Code != 200 {
		t.Fatalf("verify-iap: %d %s", rrIAP.Code, rrIAP.Body.String())
	}

	// 2. Formal user
	formalReg, err := h.authSvc.Register(context.Background(), app, auth.RegisterInput{
		Email: "buyer@example.com", Password: "buyerpassword123",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Transfer using guest_refresh_token
	body, _ := json.Marshal(map[string]any{
		"guest_refresh_token": guestReg.Tokens.RefreshToken,
	})
	req := httptest.NewRequest("POST", "/api/v1/billing/transfer-guest-credits", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+formalReg.Tokens.AccessToken)
	req.Header.Set("X-App-ID", app.ID.String())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("transfer via refresh token: %d %s", rr.Code, rr.Body.String())
	}

	// 4. Verify purchase was reassigned in database
	purch, err := h.billing.Store().GetPurchaseByProviderRef(context.Background(), nil, app.ID, billing.ProviderApple, txID)
	if err != nil {
		t.Fatal(err)
	}
	if purch.UserID != formalReg.User.ID {
		t.Fatalf("purchase user_id should be reassigned to formal user %s, got %s", formalReg.User.ID, purch.UserID)
	}

	// 5. Formal user calling verify-iap on their newly acquired purchase should return already_processed = true
	rrRepeatVerify := h.verifyApple(t, formalReg.Tokens.AccessToken, app.ID, txID)
	if rrRepeatVerify.Code != 200 {
		t.Fatalf("repeat verify after transfer: %d %s", rrRepeatVerify.Code, rrRepeatVerify.Body.String())
	}
	var iapRes struct {
		AlreadyProcessed bool  `json:"already_processed"`
		Balance          int64 `json:"balance"`
	}
	_ = json.Unmarshal(rrRepeatVerify.Body.Bytes(), &iapRes)
	if !iapRes.AlreadyProcessed {
		t.Errorf("expected already_processed true when formal user verifies reassigned IAP")
	}
	if iapRes.Balance != 300 {
		t.Errorf("expected balance 300, got %d", iapRes.Balance)
	}
}
