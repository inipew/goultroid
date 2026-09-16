package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	saved *Account
}

func (m *mockRepo) GetActive(ctx context.Context) (*Account, error)              { return m.saved, nil }
func (m *mockRepo) GetByMSISDN(ctx context.Context, id string) (*Account, error) { return m.saved, nil }
func (m *mockRepo) List(ctx context.Context) ([]*Account, error)                 { return []*Account{m.saved}, nil }
func (m *mockRepo) Save(ctx context.Context, acc *Account) error {
	m.saved = acc
	return nil
}
func (m *mockRepo) SetActive(ctx context.Context, id string) error       { return nil }
func (m *mockRepo) SetAlias(ctx context.Context, id, alias string) error { return nil }
func (m *mockRepo) Delete(ctx context.Context, id string) error          { return nil }

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
		MSISDN:       "6281912345678",
		IDToken:      "expired_id_token",
		RefreshToken: "valid_refresh_token",
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
