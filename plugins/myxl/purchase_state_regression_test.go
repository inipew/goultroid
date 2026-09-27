package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
)

func TestPurchaseIntentKeepsLookupCodeWhileCanonicalRefreshes(t *testing.T) {
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
		call := detailCalls.Add(1)
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

		canonicalCode := "OPT-CANON-A"
		token := "TOKEN-A"
		if call > 1 {
			canonicalCode = "OPT-CANON-B"
			token = "TOKEN-B"
		}
		payload, err := json.Marshal(map[string]any{
			"status":  "SUCCESS",
			"message": "",
			"data": map[string]any{
				"package_family": map[string]any{
					"name":                "Family",
					"package_family_code": "FAM",
				},
				"package_option": map[string]any{
					"name":                "Paket Canonical",
					"package_option_code": canonicalCode,
					"price":               25000,
				},
				"token_confirmation": token,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		xTime := time.Now().UnixMilli()
		xData, _ := EncryptXData(string(payload), xTime, DefaultXDataKey)
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
	if intent.LookupOptionCode != "OPT-SAVED-OLD" || intent.OptionCode != "OPT-CANON-A" {
		t.Fatalf("prepared intent = %+v", intent)
	}
	if quote.Intent.OptionCode != "OPT-CANON-A" || quote.Intent.LookupOptionCode != "OPT-SAVED-OLD" {
		t.Fatalf("quote intent = %+v", quote.Intent)
	}

	resolved, err := p.resolvePurchaseIntent(ctx, intent)
	if err != nil {
		t.Fatalf("resolvePurchaseIntent() error = %v", err)
	}
	if resolved.Item.ItemCode != "OPT-CANON-B" {
		t.Fatalf("fresh settlement item = %q, want OPT-CANON-B", resolved.Item.ItemCode)
	}
	if resolved.Intent.OptionCode != "OPT-CANON-B" || resolved.Intent.LookupOptionCode != "OPT-SAVED-OLD" {
		t.Fatalf("fresh resolved intent = %+v", resolved.Intent)
	}
	if resolved.Item.TokenConfirmation != "TOKEN-B" {
		t.Fatalf("fresh confirmation token = %q, want TOKEN-B", resolved.Item.TokenConfirmation)
	}
	if detailCalls.Load() != 2 {
		t.Fatalf("detail calls = %d, want prepare + fresh resolve", detailCalls.Load())
	}
}

func TestFreshPurchaseCanonicalCodeUsesLatestDetail(t *testing.T) {
	intent := purchaseIntentState{OptionCode: "OPT-CANON-A", LookupOptionCode: "OPT-SAVED-OLD"}

	if got := freshPurchaseCanonicalCode(intent, ""); got != "OPT-CANON-A" {
		t.Fatalf("missing echo = %q, want existing canonical code", got)
	}
	if got := freshPurchaseCanonicalCode(intent, "OPT-CANON-B"); got != "OPT-CANON-B" {
		t.Fatalf("fresh echo = %q, want OPT-CANON-B", got)
	}
}
