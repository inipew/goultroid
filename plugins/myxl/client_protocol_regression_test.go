package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/platform/network"
)

func TestGetPackageDetailsUsesCurrentEngselContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v8/xl-stores/options/detail" {
			http.NotFound(w, r)
			return
		}
		var env EncryptedBody
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		plain, err := DecryptXData(env.XData, env.XTime, DefaultXDataKey)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(plain), &payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["package_code"]; exists {
			t.Fatal("detail payload must not send legacy package_code")
		}
		for key, want := range map[string]any{
			"is_transaction_routine": false,
			"migration_type":         "NONE",
			"package_family_code":    "",
			"family_role_hub":        "",
			"is_autobuy":             false,
			"is_enterprise":          false,
			"is_shareable":           false,
			"is_migration":           false,
			"lang":                   "en",
			"package_option_code":    "OPT-A",
			"is_upsell_pdp":          false,
			"package_variant_code":   "",
		} {
			if got := payload[key]; got != want {
				t.Errorf("payload[%q] = %#v, want %#v", key, got, want)
			}
		}
		response := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Family","package_family_code":"FAM"},"package_option":{"name":"Paket A","package_option_code":"OPT-A","price":25000},"token_confirmation":"CONF-A"}}`
		xTime := time.Now().UnixMilli()
		xData, _ := EncryptXData(response, xTime, DefaultXDataKey)
		_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	repo := &mockRepo{}
	client := NewClient(cfg, repo, network.NewService(server.Client(), nil).ForOwner("myxl"))
	acc := &Account{
		MSISDN:         "6281912345678",
		AccessToken:    "access-token",
		IDToken:        "id-token",
		RefreshToken:   "refresh-token",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	repo.saved = acc

	details, err := client.GetPackageDetails(context.Background(), acc, "OPT-A")
	if err != nil {
		t.Fatalf("GetPackageDetails() error = %v", err)
	}
	if details.PackageOption == nil || details.PackageOption.PackageOptionCode != "OPT-A" {
		t.Fatalf("details = %+v", details)
	}
}

func TestSettlementBalanceSeparatesSignatureAndEnvelopeTimes(t *testing.T) {
	const (
		paymentSignatureTime = int64(1234567)
		paymentFor           = "SPECIAL_PURCHASE"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/payments/api/v8/payment-methods-option":
			data, _ := json.Marshal(PaymentMethodsOptionData{
				TokenPayment: "TP-A",
				PaymentFor:   paymentFor,
				Timestamp:    float64(paymentSignatureTime),
			})
			_ = json.NewEncoder(w).Encode(APIResponse{Status: "SUCCESS", Data: data})
		case "/payments/api/v8/settlement-multipayment":
			var env EncryptedBody
			if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
				t.Fatal(err)
			}
			wantHeaderTime := strconv.FormatInt(env.XTime/1000, 10)
			if got := r.Header.Get("x-signature-time"); got != wantHeaderTime {
				t.Errorf("x-signature-time = %q, want envelope time %q", got, wantHeaderTime)
			}
			params := PaymentSignatureParams{
				AccessToken:    "access-token",
				SigTimeSec:     paymentSignatureTime,
				PackageCode:    "OPT-A",
				TokenPayment:   "TP-A",
				PaymentMethod:  "BALANCE",
				PaymentFor:     paymentFor,
				Path:           "payments/api/v8/settlement-multipayment",
				XAPIBaseSecret: DefaultXAPIBaseSecret,
			}
			if got, want := r.Header.Get("x-signature"), MakeXSignaturePaymentParams(params, DefaultPaymentSigSecret); got != want {
				t.Fatalf("payment signature mismatch")
			}
			plain, err := DecryptXData(env.XData, env.XTime, DefaultXDataKey)
			if err != nil {
				t.Fatal(err)
			}
			var req SettlementBalanceRequest
			if err := json.Unmarshal([]byte(plain), &req); err != nil {
				t.Fatal(err)
			}
			if req.PaymentFor != paymentFor {
				t.Fatalf("payment_for = %q, want %q", req.PaymentFor, paymentFor)
			}
			if req.Timestamp == paymentSignatureTime {
				t.Fatalf("settlement body reused payment signature timestamp %d", req.Timestamp)
			}
			if delta := time.Now().Unix() - req.Timestamp; delta < 0 || delta > 5 {
				t.Fatalf("settlement timestamp = %d, want current unix time", req.Timestamp)
			}
			data, _ := json.Marshal(SettlementData{TransactionCode: "TRX-OK"})
			_ = json.NewEncoder(w).Encode(APIResponse{Status: "SUCCESS", Data: data})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	repo := &mockRepo{}
	client := NewClient(cfg, repo, network.NewService(server.Client(), nil).ForOwner("myxl"))
	acc := &Account{
		MSISDN:         "6281912345678",
		AccessToken:    "access-token",
		IDToken:        "id-token",
		RefreshToken:   "refresh-token",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}
	repo.saved = acc

	res, err := client.SettlementBalance(context.Background(), acc, PurchaseItem{
		ItemCode:          "OPT-A",
		ItemPrice:         25000,
		ItemName:          "Paket A",
		TokenConfirmation: "CONF-A",
	}, nil)
	if err != nil {
		t.Fatalf("SettlementBalance() error = %v", err)
	}
	if !res.IsSuccess || res.TransactionCode != "TRX-OK" {
		t.Fatalf("result = %+v", res)
	}
}
