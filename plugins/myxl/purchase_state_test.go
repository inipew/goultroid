package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/services/callback"
)

type purchaseQuoteFixture struct {
	mu    sync.Mutex
	price int64
	token string
	name  string
}

func (f *purchaseQuoteFixture) set(price int64, token, name string) {
	f.mu.Lock()
	f.price = price
	f.token = token
	f.name = name
	f.mu.Unlock()
}

func (f *purchaseQuoteFixture) snapshot() (int64, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.price, f.token, f.name
}

func TestP1E2PurchaseIntentUsesFreshTokenAndFencesPriceDrift(t *testing.T) {
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

	fixture := &purchaseQuoteFixture{price: 25000, token: "TOKEN-A", name: "Paket A"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v8/xl-stores/options/detail" {
			http.NotFound(w, r)
			return
		}
		price, token, name := fixture.snapshot()
		payload, err := json.Marshal(map[string]any{
			"status":  "SUCCESS",
			"message": "",
			"data": map[string]any{
				"package_family": map[string]any{
					"name":                "Family",
					"package_family_code": "FAM",
				},
				"package_option": map[string]any{
					"name":                name,
					"package_option_code": "OPT-A",
					"price":               price,
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
		OptionCode: "OPT-A",
		Method:     "balance",
	})
	if err != nil {
		t.Fatalf("preparePurchaseIntent() error = %v", err)
	}
	if quote.PackageName != "Paket A" || quote.CanonicalPrice != 25000 || quote.EffectivePrice != 25000 {
		t.Fatalf("quote = %+v", quote)
	}
	if intent.QuotedPrice != 25000 {
		t.Fatalf("QuotedPrice = %d, want 25000", intent.QuotedPrice)
	}

	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 512 {
		t.Fatalf("purchase intent retained %d bytes, want <= 512", len(raw))
	}
	for _, forbidden := range []string{"TOKEN-A", "Paket A", "token_confirmation", "package_name"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("purchase intent leaked transient authority %q: %s", forbidden, raw)
		}
	}

	resolvedA, err := p.resolvePurchaseIntent(ctx, intent)
	if err != nil {
		t.Fatalf("resolve initial quote: %v", err)
	}
	if resolvedA.Item.TokenConfirmation != "TOKEN-A" {
		t.Fatalf("initial token = %q", resolvedA.Item.TokenConfirmation)
	}

	fixture.set(25000, "TOKEN-B", "Paket A")
	resolvedB, err := p.resolvePurchaseIntent(ctx, intent)
	if err != nil {
		t.Fatalf("resolve rotated token: %v", err)
	}
	if resolvedB.Item.TokenConfirmation != "TOKEN-B" {
		t.Fatalf("fresh token = %q, want TOKEN-B", resolvedB.Item.TokenConfirmation)
	}
	if resolvedA.IdempotencyKey == resolvedB.IdempotencyKey {
		t.Fatal("idempotency key did not rotate with fresh confirmation token")
	}

	fixture.set(26000, "TOKEN-C", "Paket A")
	if _, err := p.resolvePurchaseIntent(ctx, intent); !errors.Is(err, ErrPurchaseQuoteChanged) {
		t.Fatalf("price drift error = %v, want ErrPurchaseQuoteChanged", err)
	}

	cb := &callback.CallbackContext{
		Ctx:     ctx,
		QueryID: 100,
		UserID:  1,
		Service: &core.MockTelegramServicer{},
		Target: core.CallbackTarget{
			Peer:      &tg.InputPeerUser{UserID: 1},
			MessageID: 10,
		},
	}
	if err := p.confirmPurchase(cb, intent); err != nil {
		t.Fatalf("confirmPurchase price drift response error = %v", err)
	}
	var reservations int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM myxl_purchase_requests").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("price drift created %d purchase reservations, want 0", reservations)
	}
}

func TestP1E2LegacyPurchaseDraftConvertsWithoutTrustingSnapshotAuthority(t *testing.T) {
	legacy := purchaseDraftState{
		MSISDN:            "6281912345678",
		OptionCode:        "OPT-A",
		PackageName:       "Stale Package Name",
		Price:             25000,
		TokenConfirmation: "STALE-TOKEN",
		Method:            "balance",
		WalletNumber:      "6281912345678",
		OverwritePrice:    1000,
		HasOverwrite:      true,
	}
	intent, ok := purchaseIntentFromCallbackState(legacy)
	if !ok {
		t.Fatal("legacy draft was not accepted for compatibility")
	}
	if intent.MSISDN != legacy.MSISDN || intent.OptionCode != legacy.OptionCode || intent.QuotedPrice != legacy.Price {
		t.Fatalf("compatibility intent = %+v", intent)
	}
	if intent.WalletNumber != "" {
		t.Fatalf("non-wallet method retained wallet number %q", intent.WalletNumber)
	}
	typ := reflect.TypeOf(intent)
	for _, forbidden := range []string{"TokenConfirmation", "PackageName", "Price"} {
		if _, exists := typ.FieldByName(forbidden); exists {
			t.Fatalf("purchaseIntentState must not contain legacy authority field %s", forbidden)
		}
	}
}

func TestP1E2PurchaseIntentValidationFailsClosed(t *testing.T) {
	for _, tc := range []purchaseIntentState{
		{MSISDN: "", OptionCode: "OPT", Method: "balance"},
		{MSISDN: "6281912345678", OptionCode: "", Method: "balance"},
		{MSISDN: "6281912345678", OptionCode: "OPT", Method: "unknown"},
		{MSISDN: "6281912345678", OptionCode: "OPT", Method: "balance", QuotedPrice: -1},
		{MSISDN: "6281912345678", OptionCode: "OPT", Method: "balance", HasOverwrite: true, OverwritePrice: -1},
		{MSISDN: "6281912345678", OptionCode: "OPT", Method: "dana", WalletNumber: "not-a-number"},
	} {
		if _, err := normalizePurchaseIntent(tc); !errors.Is(err, ErrPurchaseIntentInvalid) {
			t.Fatalf("normalizePurchaseIntent(%+v) error = %v, want ErrPurchaseIntentInvalid", tc, err)
		}
	}
}
