package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/platform/network"
)

func TestNormalizeMSISDN(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"081912345678", "6281912345678", false},
		{"+6281912345678", "6281912345678", false},
		{"62819-1234-5678", "6281912345678", false},
		{"81912345678", "6281912345678", false},
		{"12345", "", true},
		{"021123456", "", true},
	}

	for _, tc := range tests {
		got, err := NormalizeMSISDN(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("NormalizeMSISDN(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeMSISDN(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

type mockRepo struct {
	mu      sync.Mutex
	saved   *Account
	saveErr error
	getErr  error
}

func (m *mockRepo) GetActive(ctx context.Context) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.saved == nil {
		return nil, nil
	}
	clone := *m.saved
	return &clone, nil
}
func (m *mockRepo) GetByMSISDN(ctx context.Context, id string) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.saved == nil {
		return nil, nil
	}
	clone := *m.saved
	return &clone, nil
}
func (m *mockRepo) List(ctx context.Context) ([]*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saved == nil {
		return nil, nil
	}
	clone := *m.saved
	return []*Account{&clone}, nil
}
func (m *mockRepo) Save(ctx context.Context, acc *Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	clone := *acc
	m.saved = &clone
	return nil
}
func (m *mockRepo) SetActive(ctx context.Context, id string) error       { return nil }
func (m *mockRepo) SetAlias(ctx context.Context, id, alias string) error { return nil }
func (m *mockRepo) Delete(ctx context.Context, id string) error          { return nil }

func (m *mockRepo) SavePackage(ctx context.Context, pkg *SavedPackage) error { return nil }
func (m *mockRepo) GetSavedPackages(ctx context.Context, msisdn string) ([]*SavedPackage, error) {
	return nil, nil
}
func (m *mockRepo) GetSavedPackage(ctx context.Context, msisdn, optionCode string) (*SavedPackage, error) {
	return nil, nil
}
func (m *mockRepo) DeleteSavedPackage(ctx context.Context, msisdn, optionCode string) error {
	return nil
}
func (m *mockRepo) ReservePurchase(ctx context.Context, key, msisdn, optionCode, paymentMethod string) (bool, error) {
	return true, nil
}
func (m *mockRepo) FinishPurchase(ctx context.Context, key, status, transactionCode, message string) error {
	return nil
}
func (m *mockRepo) GetDecoy(ctx context.Context, key string) (*DecoyConfig, error) {
	return nil, nil
}
func (m *mockRepo) UpsertDecoy(ctx context.Context, decoy *DecoyConfig) error {
	return nil
}

func TestClient_RequestAndSubmitOTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/xl-ciam/auth/otp":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subscriber_id": "SUB-9999",
			})
		case "/realms/xl-ciam/protocol/openid-connect/token":
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "access_token_mock",
				IDToken:      "id_token_mock",
				RefreshToken: "refresh_token_mock",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	ctx := context.Background()
	subID, err := client.RequestOTP(ctx, "081912345678")
	if err != nil {
		t.Fatalf("RequestOTP failed: %v", err)
	}
	if subID != "SUB-9999" {
		t.Fatalf("expected subscriber_id SUB-9999, got %q", subID)
	}

	tokens, err := client.SubmitOTP(ctx, "081912345678", "123456")
	if err != nil {
		t.Fatalf("SubmitOTP failed: %v", err)
	}
	if tokens.IDToken != "id_token_mock" {
		t.Fatalf("expected id_token id_token_mock, got %q", tokens.IDToken)
	}
}

func TestClient_ExecuteEngsel_AutoRefreshOn401(t *testing.T) {
	refreshCalled := false
	apiCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/xl-ciam/protocol/openid-connect/token":
			refreshCalled = true
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "new_access_token",
				IDToken:      "new_id_token",
				RefreshToken: "new_refresh_token",
			})
		case "/api/v8/packages/balance-and-credit":
			apiCallCount++
			if apiCallCount == 1 {
				// First attempt: return 401 Unauthorized
				_ = json.NewEncoder(w).Encode(APIResponse{
					Status:  "401",
					Message: "Token Expired",
				})
				return
			}
			// Second attempt: encrypt a valid balance response
			payload := `{"status":"SUCCESS","message":"","data":{"balance":{"remaining":100000,"expired_at":1735689600}}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{
				XData: xdata,
				XTime: xtime,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	cfg.BaseAPIURL = server.URL

	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:         "6281912345678",
		IDToken:        "expired_id_token",
		RefreshToken:   "valid_refresh_token",
		TokenExpiresAt: time.Now().Add(1 * time.Hour),
	}

	bal, err := client.GetBalance(context.Background(), acc)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if !refreshCalled {
		t.Error("expected RefreshToken to be called on 401")
	}
	if apiCallCount != 2 {
		t.Errorf("expected 2 API attempts (initial + retry), got %d", apiCallCount)
	}
	if bal.Remaining != 100000 {
		t.Errorf("expected balance 100000, got %f", bal.Remaining)
	}
	if acc.IDToken != "new_id_token" {
		t.Errorf("expected account IDToken to be updated, got %q", acc.IDToken)
	}
}

func TestClient_OTPCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"subscriber_id": "SUB-123"})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	ctx := context.Background()
	_, err := client.RequestOTP(ctx, "081912345678")
	if err != nil {
		t.Fatalf("first OTP request failed: %v", err)
	}

	// Immediate second call should trigger cooldown
	_, err2 := client.RequestOTP(ctx, "081912345678")
	if err2 == nil || !strings.Contains(err2.Error(), "tunggu") {
		t.Fatalf("expected cooldown error on second OTP request, got %v", err2)
	}
}

func TestClient_UpdateConfig(t *testing.T) {
	cfg := DefaultClientConfig()
	client := NewClient(cfg, nil, nil)
	client.UpdateConfig(func(c *ClientConfig) {
		c.APIKey = "custom_key_123"
	})
	if client.cfg.APIKey != "custom_key_123" {
		t.Fatalf("expected APIKey custom_key_123, got %s", client.cfg.APIKey)
	}
}

func TestClient_SearchAndSettlements(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v8/xl-stores/options/list":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data: json.RawMessage(`{
					"package_family": {"name": "Akrab", "package_family_code": "FAM-1"},
					"package_variants": [{
						"name": "Variant A",
						"package_variant_code": "VAR-A",
						"package_options": [{"name": "Option 1", "package_option_code": "OPT-1", "price": 25000}]
					}]
				}`),
			})
		case "/api/v8/xl-stores/options/detail":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data: json.RawMessage(`{
					"package_family": {"name": "Akrab", "package_family_code": "FAM-1"},
					"package_option": {"name": "Option 1", "package_option_code": "OPT-1", "price": 25000},
					"token_confirmation": "CONF-123"
				}`),
			})
		case "/payments/api/v8/payment-methods-option":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data: json.RawMessage(`{
					"token_payment": "TOK-PAY-999",
					"payment_for": "BUY_PACKAGE",
					"payment_method": "BALANCE",
					"price": 25000,
					"timestamp": 1700000000
				}`),
			})
		case "/payments/api/v8/settlement-multipayment":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data:   json.RawMessage(`{"transaction_code": "TRX-BALANCE-100"}`),
			})
		case "/payments/api/v8/settlement-multipayment/ewallet":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data:   json.RawMessage(`{"transaction_code": "TRX-GOPAY-200", "deeplink": "https://pay.gopay.id/200"}`),
			})
		case "/payments/api/v8/settlement-multipayment/qris":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data:   json.RawMessage(`{"transaction_code": "TRX-QRIS-300"}`),
			})
		case "/payments/api/v8/pending-detail":
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data:   json.RawMessage(`{"qr_code": "00020101021226590014ID.LINKAJA.WWW..."}`),
			})
		case "/realms/xl-ciam/protocol/openid-connect/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new_access_token",
				"id_token":      "new_id_token",
				"refresh_token": "new_refresh_token",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	cfg.BaseCIAMURL = server.URL

	acc := &Account{
		MSISDN:       "6281900000001",
		Alias:        "TestAcc",
		AccessToken:  "acc_token",
		IDToken:      "id_token",
		RefreshToken: "ref_token",
	}
	repo := &mockRepo{saved: acc}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)
	ctx := context.Background()

	// 1. Test GetPackagesByFamily
	famResp, err := client.GetPackagesByFamily(ctx, acc, "FAM-1")
	if err != nil || famResp.PackageFamily.Name != "Akrab" || len(famResp.PackageVariants) != 1 {
		t.Fatalf("GetPackagesByFamily failed: %#v (err: %v)", famResp, err)
	}

	// 2. Test GetPackageDetails
	detailResp, err := client.GetPackageDetails(ctx, acc, "OPT-1")
	if err != nil || detailResp.TokenConfirmation != "CONF-123" {
		t.Fatalf("GetPackageDetails failed: %#v (err: %v)", detailResp, err)
	}

	// 3. Test SettlementBalance with overwrite price 0
	overwritePrice := int64(0)
	balRes, err := client.SettlementBalance(ctx, acc, PurchaseItem{
		ItemCode:          "OPT-1",
		ItemPrice:         25000,
		ItemName:          "Option 1",
		TokenConfirmation: "CONF-123",
	}, &overwritePrice)
	if err != nil || !balRes.IsSuccess || balRes.TransactionCode != "TRX-BALANCE-100" {
		t.Fatalf("SettlementBalance failed: %#v (err: %v)", balRes, err)
	}

	// 4. Test SettlementMultipayment (GoPay)
	ewalletRes, err := client.SettlementMultipayment(ctx, acc, PurchaseItem{
		ItemCode:          "OPT-1",
		ItemPrice:         25000,
		ItemName:          "Option 1",
		TokenConfirmation: "CONF-123",
	}, "GOPAY", "", nil)
	if err != nil || !ewalletRes.IsSuccess || ewalletRes.Deeplink != "https://pay.gopay.id/200" {
		t.Fatalf("SettlementMultipayment failed: %#v (err: %v)", ewalletRes, err)
	}

	// 5. Test SettlementQRIS
	qrisRes, err := client.SettlementQRIS(ctx, acc, PurchaseItem{
		ItemCode:          "OPT-1",
		ItemPrice:         25000,
		ItemName:          "Option 1",
		TokenConfirmation: "CONF-123",
	}, nil)
	if err != nil || !qrisRes.IsSuccess || qrisRes.QRCode == "" {
		t.Fatalf("SettlementQRIS failed: %#v (err: %v)", qrisRes, err)
	}

	// 6. Test ForceRefreshToken
	refreshed, err := client.ForceRefreshToken(ctx, "6281900000001")
	if err != nil || refreshed.AccessToken != "new_access_token" {
		t.Fatalf("ForceRefreshToken failed: %#v (err: %v)", refreshed, err)
	}
	if repo.saved.AccessToken != "new_access_token" {
		t.Fatalf("expected updated access token in repo, got %s", repo.saved.AccessToken)
	}
}

func TestClient_EnsureFreshToken_Proactive(t *testing.T) {
	refreshCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/realms/xl-ciam/protocol/openid-connect/token" {
			atomic.AddInt32(&refreshCount, 1)
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "proactive_access_token",
				IDToken:      "proactive_id_token",
				RefreshToken: "proactive_refresh_token",
				ExpiresIn:    3600,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	// Token expires in 60 seconds (< DefaultTokenRefreshSkew = 2m) -> should trigger proactive refresh
	acc := &Account{
		MSISDN:         "6281911122233",
		AccessToken:    "old_access_token",
		IDToken:        "old_id_token",
		RefreshToken:   "old_refresh_token",
		TokenExpiresAt: time.Now().Add(60 * time.Second),
	}
	repo.saved = acc

	if err := client.EnsureFreshToken(context.Background(), acc); err != nil {
		t.Fatalf("EnsureFreshToken failed: %v", err)
	}

	if atomic.LoadInt32(&refreshCount) != 1 {
		t.Fatalf("expected 1 proactive refresh call, got %d", refreshCount)
	}
	if acc.AccessToken != "proactive_access_token" {
		t.Fatalf("expected updated access token, got %s", acc.AccessToken)
	}
	if time.Until(acc.TokenExpiresAt) < 3000*time.Second {
		t.Fatalf("expected TokenExpiresAt ~3600s in future, got %v", time.Until(acc.TokenExpiresAt))
	}

	// Immediate second call: token is still valid (> 2m) -> should NOT refresh again
	if err := client.EnsureFreshToken(context.Background(), acc); err != nil {
		t.Fatalf("second EnsureFreshToken failed: %v", err)
	}
	if atomic.LoadInt32(&refreshCount) != 1 {
		t.Fatalf("expected still 1 refresh call, got %d", refreshCount)
	}
}

func TestClient_SingleflightConcurrency(t *testing.T) {
	refreshCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/realms/xl-ciam/protocol/openid-connect/token" {
			atomic.AddInt32(&refreshCount, 1)
			time.Sleep(50 * time.Millisecond) // simulate network latency
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "singleflight_access_token",
				IDToken:      "singleflight_id_token",
				RefreshToken: "singleflight_refresh_token",
				ExpiresIn:    3600,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:         "6281999988877",
		AccessToken:    "expired_token",
		RefreshToken:   "rt_valid",
		TokenExpiresAt: time.Now().Add(-10 * time.Minute), // expired
	}
	repo.saved = acc

	var wg sync.WaitGroup
	concurrentRequests := 20
	errChan := make(chan error, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reqAcc := Account{
				MSISDN:         "6281999988877",
				AccessToken:    "expired_token",
				RefreshToken:   "rt_valid",
				TokenExpiresAt: time.Now().Add(-10 * time.Minute),
			}
			err := client.EnsureFreshToken(context.Background(), &reqAcc)
			if err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatalf("concurrent EnsureFreshToken returned error: %v", err)
	}

	// Critical check: Singleflight must have coalesced all 20 calls into EXACTLY 1 refresh!
	if calls := atomic.LoadInt32(&refreshCount); calls != 1 {
		t.Fatalf("singleflight failed! Expected exactly 1 CIAM refresh call, got %d", calls)
	}
}

func TestClient_Settlement_RebuildPayloadOn401(t *testing.T) {
	refreshCalled := false
	optCallCount := 0
	paymentCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/xl-ciam/protocol/openid-connect/token":
			refreshCalled = true
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "fresh_payment_access_token",
				IDToken:      "fresh_payment_id_token",
				RefreshToken: "fresh_payment_refresh_token",
				ExpiresIn:    3600,
			})
		case "/payments/api/v8/payment-methods-option":
			optCallCount++
			var ts float64 = 1000
			tp := "TP-1"
			if optCallCount == 2 {
				ts = 2000
				tp = "TP-2"
			}
			data := PaymentMethodsOptionData{
				TokenPayment: tp,
				Timestamp:    ts,
			}
			b, _ := json.Marshal(data)
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status:  "SUCCESS",
				Message: "",
				Data:    b,
			})
		case "/payments/api/v8/settlement-multipayment":
			paymentCallCount++
			authHeader := r.Header.Get("authorization")
			if paymentCallCount == 1 {
				if authHeader != "Bearer expired_id_token" {
					t.Errorf("call 1: expected Bearer expired_id_token, got %s", authHeader)
				}
				// First payment call rejected with 401
				_ = json.NewEncoder(w).Encode(APIResponse{
					Status:  "401",
					Message: "Payment Token Expired",
				})
				return
			}

			// Call 2: Must be rebuilt with fresh tokens and matching fresh timestamp!
			if authHeader != "Bearer fresh_payment_id_token" {
				t.Errorf("call 2: expected Bearer fresh_payment_id_token, got %s", authHeader)
			}
			sigTime := r.Header.Get("x-signature-time")
			if sigTime != "2000" {
				t.Errorf("call 2: expected x-signature-time 2000, got %s", sigTime)
			}

			// Read body and verify inner JSON has fresh access token & fresh timestamp
			var env EncryptedBody
			_ = json.NewDecoder(r.Body).Decode(&env)
			decrypted, err := DecryptXData(env.XData, env.XTime, DefaultXDataKey)
			if err != nil {
				t.Fatalf("decrypt payment body: %v", err)
			}

			var req SettlementBalanceRequest
			if err := json.Unmarshal([]byte(decrypted), &req); err != nil {
				t.Fatalf("unmarshal decrypted payment body: %v", err)
			}

			if req.AccessToken != "fresh_payment_access_token" {
				t.Errorf("call 2: expected body access_token fresh_payment_access_token, got %s", req.AccessToken)
			}
			if req.Timestamp != 2000 {
				t.Errorf("call 2: expected body timestamp 2000, got %d", req.Timestamp)
			}
			if req.TokenPayment != "TP-2" {
				t.Errorf("call 2: expected body token_payment TP-2, got %s", req.TokenPayment)
			}

			payload := `{"status":"SUCCESS","message":"Payment Successful","data":{"transaction_code":"TRX-REBUILD-OK"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{
				XData: xdata,
				XTime: xtime,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	cfg.BaseAPIURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:         "6281988877766",
		AccessToken:    "expired_access_token",
		IDToken:        "expired_id_token",
		RefreshToken:   "valid_refresh_token",
		TokenExpiresAt: time.Now().Add(1 * time.Hour), // client assumed valid, server rejected with 401
	}
	repo.saved = acc

	res, err := client.SettlementBalance(context.Background(), acc, PurchaseItem{
		ItemCode:          "OPT-TEST",
		ItemPrice:         50000,
		ItemName:          "Test Package",
		TokenConfirmation: "CONF-999",
	}, nil)
	if err != nil {
		t.Fatalf("SettlementBalance failed: %v", err)
	}

	if !refreshCalled {
		t.Fatal("expected settlement retry to trigger token refresh on 401")
	}
	if optCallCount != 2 {
		t.Fatalf("expected 2 GetPaymentMethodsOption calls (initial + rebuild), got %d", optCallCount)
	}
	if paymentCallCount != 2 {
		t.Fatalf("expected 2 payment calls (initial + retry), got %d", paymentCallCount)
	}
	if !res.IsSuccess || res.TransactionCode != "TRX-REBUILD-OK" {
		t.Fatalf("expected successful settlement with TRX-REBUILD-OK, got: %#v", res)
	}
	if acc.AccessToken != "fresh_payment_access_token" {
		t.Fatalf("expected updated access token on account, got %s", acc.AccessToken)
	}
}

func TestClient_Refresh_RepoSaveFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "token_new",
			IDToken:      "id_new",
			RefreshToken: "rt_new",
			ExpiresIn:    3600,
		})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{
		saveErr: errors.New("disk full: sqlite save failed"),
	}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:       "6281900011122",
		RefreshToken: "rt_old",
	}
	repo.saved = acc

	_, err := client.RefreshToken(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error when repo.Save fails, got nil")
	}
	if !strings.Contains(err.Error(), "persist refreshed account") {
		t.Fatalf("expected persist error, got: %v", err)
	}
}

func TestClient_Refresh_RepoGetFailure(t *testing.T) {
	cfg := DefaultClientConfig()
	repo := &mockRepo{
		getErr: errors.New("sqlite connection busy"),
	}
	netCli := network.NewService(nil, nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:       "6281900011122",
		RefreshToken: "rt_old",
	}

	_, err := client.RefreshToken(context.Background(), acc)
	if err == nil {
		t.Fatal("expected error when repo.GetByMSISDN fails, got nil")
	}
	if !strings.Contains(err.Error(), "read account from repository") {
		t.Fatalf("expected repository read error, got: %v", err)
	}
}

func TestClient_Sequential401_StaleTokenGeneration(t *testing.T) {
	refreshCount := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCount, 1)
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "gen1_access_token",
			IDToken:      "gen1_id_token",
			RefreshToken: "gen1_refresh_token",
			ExpiresIn:    3600,
		})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:       "6281977766655",
		IDToken:      "gen0_id_token",
		RefreshToken: "gen0_refresh_token",
	}
	repo.saved = acc

	// Request 1: Fails with gen0_id_token -> triggers refresh
	res1, err := client.refreshAfterUnauthorized(context.Background(), acc, "gen0_id_token")
	if err != nil || res1.IDToken != "gen1_id_token" {
		t.Fatalf("refresh 1 failed: %v", err)
	}

	// Requests 2..5: Arrive sequentially, each having failed with gen0_id_token
	for i := 2; i <= 5; i++ {
		reqAcc := Account{
			MSISDN:       "6281977766655",
			IDToken:      "gen0_id_token",
			RefreshToken: "gen0_refresh_token",
		}
		res, err := client.refreshAfterUnauthorized(context.Background(), &reqAcc, "gen0_id_token")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if res.IDToken != "gen1_id_token" {
			t.Fatalf("request %d: expected gen1_id_token, got %s", i, res.IDToken)
		}
	}

	// CRITICAL: Across all 5 sequential 401 requests with gen0_id_token, exactly 1 CIAM refresh must occur!
	if calls := atomic.LoadInt32(&refreshCount); calls != 1 {
		t.Fatalf("stale generation check failed! Expected exactly 1 CIAM refresh, got %d", calls)
	}
}

func TestClient_LeaderContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Slow network
		_ = json.NewEncoder(w).Encode(Tokens{
			AccessToken:  "coalesced_access_token",
			IDToken:      "coalesced_id_token",
			RefreshToken: "coalesced_refresh_token",
			ExpiresIn:    3600,
		})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:         "6281955544433",
		RefreshToken:   "rt_valid",
		TokenExpiresAt: time.Now().Add(-1 * time.Hour), // Expired
	}
	repo.saved = acc

	var wg sync.WaitGroup
	wg.Add(2)

	var leaderErr, waiterErr error
	var waiterAcc Account

	// Leader: short 10ms context that cancels while refresh is running
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		leaderAcc := *acc
		leaderErr = client.EnsureFreshToken(ctx, &leaderAcc)
	}()

	// Waiter: healthy 3-second context
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond) // Ensure leader initiates flight first
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		waiterAcc = *acc
		waiterErr = client.EnsureFreshToken(ctx, &waiterAcc)
	}()

	wg.Wait()

	// Leader must fail with context cancellation/deadline
	if leaderErr == nil {
		t.Error("expected leader to fail with context cancellation")
	}

	// Waiter MUST succeed! Leader's timeout must not poison waiter
	if waiterErr != nil {
		t.Fatalf("waiter failed due to leader context poisoning: %v", waiterErr)
	}
	if waiterAcc.AccessToken != "coalesced_access_token" {
		t.Fatalf("expected waiter to receive fresh access token, got %s", waiterAcc.AccessToken)
	}
}

func TestParseValidAmount(t *testing.T) {
	tests := []struct {
		input   string
		wantVal int64
		wantOk  bool
	}{
		{"Bizz-err.Amount.Total=12345", 12345, true},
		{"Bizz-err.Amount.Total = 15000", 15000, true},
		{"Error: Bizz-err.Amount.Total=0.", 0, true},
		{"Bizz-err.Amount.Total=99000;", 99000, true},
		{"No equals sign here", 0, false},
		{"Bizz-err.Amount.Total=not_a_number", 0, false},
	}

	for _, tc := range tests {
		val, ok := parseValidAmount(tc.input)
		if ok != tc.wantOk || val != tc.wantVal {
			t.Errorf("parseValidAmount(%q) = (%d, %v), want (%d, %v)", tc.input, val, ok, tc.wantVal, tc.wantOk)
		}
	}
}

func TestClient_Settlement_BizzErrAmountTotal_Retry(t *testing.T) {
	settleCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/payments/api/v8/payment-methods-option":
			data := PaymentMethodsOptionData{
				TokenPayment: "TP-BIZZ",
				Timestamp:    1234567,
			}
			b, _ := json.Marshal(data)
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status: "SUCCESS",
				Data:   b,
			})
		case "/payments/api/v8/settlement-multipayment":
			settleCalls++
			var env EncryptedBody
			_ = json.NewDecoder(r.Body).Decode(&env)
			plain, _ := DecryptXData(env.XData, env.XTime, DefaultXDataKey)

			var req SettlementBalanceRequest
			_ = json.Unmarshal([]byte(plain), &req)

			if settleCalls == 1 {
				// Initial call with overwrite amount 0 fails with Bizz-err
				if req.TotalAmount != 0 {
					t.Errorf("expected first call total_amount 0, got %d", req.TotalAmount)
				}
				_ = json.NewEncoder(w).Encode(APIResponse{
					Status:  "FAILED",
					Message: "Bizz-err.Amount.Total=35000",
				})
				return
			}

			// Retry call should have total_amount = 35000
			if req.TotalAmount != 35000 {
				t.Errorf("expected retry call total_amount 35000, got %d", req.TotalAmount)
			}
			stData, _ := json.Marshal(SettlementData{TransactionCode: "TRX-BIZZ-OK"})
			_ = json.NewEncoder(w).Encode(APIResponse{
				Status:  "SUCCESS",
				Message: "Payment Accepted",
				Data:    stData,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	repo := &mockRepo{}
	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)

	acc := &Account{
		MSISDN:         "6281988899900",
		AccessToken:    "acc_token",
		IDToken:        "id_token",
		TokenExpiresAt: time.Now().Add(1 * time.Hour),
	}
	repo.saved = acc

	zero := int64(0)
	res, err := client.SettlementBalance(context.Background(), acc, PurchaseItem{
		ItemCode:          "OPT-BIZZ",
		ItemPrice:         50000,
		ItemName:          "Package Bizz",
		TokenConfirmation: "CONF-BIZZ",
	}, &zero)

	if err != nil {
		t.Fatalf("SettlementBalance failed: %v", err)
	}
	if settleCalls != 2 {
		t.Fatalf("expected 2 settlement calls (initial + retry), got %d", settleCalls)
	}
	if !res.IsSuccess || res.TransactionCode != "TRX-BIZZ-OK" {
		t.Fatalf("expected success with TRX-BIZZ-OK, got: %#v", res)
	}
}
