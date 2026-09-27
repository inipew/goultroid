package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
)

func TestPurchaseIntentKeepsLookupCodeWhenDetailCanonicalizes(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	const msisdn = "6281912345678"
	if err := repo.Save(ctx, &Account{
		MSISDN:         msisdn,
		IsActive:       true,
		AccessToken:    "access-token",
		IDToken:        "id-token",
		RefreshToken:   "refresh-token",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	var detailCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v8/xl-stores/options/detail" {
			http.NotFound(w, r)
			return
		}
		detailCalls.Add(1)
		var env EncryptedBody
		if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
			t.Fatal(err)
		}
		plain, err := DecryptXData(env.XData, env.XTime, DefaultXDataKey)
		if err != nil {
			t.Fatal(err)
		}
		var req map[string]any
		if err := json.Unmarshal([]byte(plain), &req); err != nil {
			t.Fatal(err)
		}
		if got := req["package_option_code"]; got != "OPT-SAVED-OLD" {
			t.Errorf("lookup option code = %#v, want OPT-SAVED-OLD", got)
		}

		payload := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Family","package_family_code":"FAM"},"package_option":{"name":"Paket Canonical","package_option_code":"OPT-CANON","price":25000},"token_confirmation":"TOKEN-A"}}`
		xTime := time.Now().UnixMilli()
		xData, _ := EncryptXData(payload, xTime, DefaultXDataKey)
		_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	p := New(repo, NewClient(cfg, repo, network.NewService(server.Client(), nil).ForOwner("myxl")))

	intent, quote, err := p.preparePurchaseIntent(ctx, purchaseIntentState{
		MSISDN:     msisdn,
		OptionCode: "OPT-SAVED-OLD",
		Method:     "balance",
	})
	if err != nil {
		t.Fatalf("preparePurchaseIntent() error = %v", err)
	}
	if intent.LookupOptionCode != "OPT-SAVED-OLD" || intent.OptionCode != "OPT-CANON" {
		t.Fatalf("canonicalized intent = %+v", intent)
	}
	if quote.Intent.OptionCode != "OPT-CANON" || quote.Intent.LookupOptionCode != "OPT-SAVED-OLD" {
		t.Fatalf("quote intent = %+v", quote.Intent)
	}

	resolved, err := p.resolvePurchaseIntent(ctx, intent)
	if err != nil {
		t.Fatalf("resolvePurchaseIntent() error = %v", err)
	}
	if resolved.Item.ItemCode != "OPT-CANON" || resolved.Intent.LookupOptionCode != "OPT-SAVED-OLD" {
		t.Fatalf("resolved purchase = %+v", resolved)
	}
	if detailCalls.Load() != 2 {
		t.Fatalf("detail calls = %d, want prepare + fresh resolve", detailCalls.Load())
	}
}

func TestPurchaseCanonicalCodeRejectsDriftAfterCanonicalization(t *testing.T) {
	intent := purchaseIntentState{
		OptionCode:       "OPT-CANON",
		LookupOptionCode: "OPT-SAVED-OLD",
	}

	if got, err := resolvePurchaseCanonicalCode(intent, ""); err != nil || got != "OPT-CANON" {
		t.Fatalf("missing echo = (%q, %v), want expected canonical code", got, err)
	}
	if got, err := resolvePurchaseCanonicalCode(intent, "opt-canon"); err != nil || got != "opt-canon" {
		t.Fatalf("matching echo = (%q, %v)", got, err)
	}
	if _, err := resolvePurchaseCanonicalCode(intent, "OPT-CHANGED"); !errors.Is(err, ErrPurchaseIntentInvalid) {
		t.Fatalf("canonical drift error = %v, want ErrPurchaseIntentInvalid", err)
	}
}
