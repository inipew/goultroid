package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/services/callback"
)

func setupTestMyXLEnv(t *testing.T) (*Plugin, *httptest.Server, *SQLiteRepository) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	ctx := context.Background()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	repo := NewSQLiteRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/xl-ciam/auth/otp":
			_ = json.NewEncoder(w).Encode(map[string]any{"subscriber_id": "SUB-TEST-1"})
		case "/realms/xl-ciam/protocol/openid-connect/token":
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "mock_acc_token",
				IDToken:      "mock_id_token",
				RefreshToken: "mock_ref_token",
				ExpiresIn:    3600,
			})
		case "/api/v8/packages/balance-and-credit":
			payload := `{"status":"SUCCESS","message":"","data":{"balance":{"remaining":50000,"expired_at":1735689600}}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/packages/quota-details":
			payload := `{"status":"SUCCESS","message":"","data":{"quotas":[{"name":"Akrab","expired_at":1735689600,"benefits":[{"name":"Utama","data_type":"DATA","remaining":2147483648,"total":5368709120}]}]}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/xl-stores/options/list":
			payload := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Xtra Combo Flex","package_family_code":"FAM-FLEX"},"package_variants":[{"name":"Flex Basic","package_variant_code":"VAR-1","package_options":[{"name":"Flex S 10GB","package_option_code":"OPT-1","price":30000},{"name":"Flex M 20GB","package_option_code":"OPT-2","price":50000},{"name":"Flex L 30GB","package_option_code":"OPT-3","price":70000},{"name":"Flex XL 50GB","package_option_code":"OPT-4","price":100000},{"name":"Flex XXL 80GB","package_option_code":"OPT-5","price":140000},{"name":"Flex Max 100GB","package_option_code":"OPT-6","price":180000},{"name":"Flex Ultra 150GB","package_option_code":"OPT-7","price":230000}]}]}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/xl-stores/options/detail":
			payload := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Xtra Combo","package_family_code":"FAM-1"},"package_option":{"name":"Combo 10GB","package_option_code":"OPT-10GB","price":25000},"token_confirmation":"TOK-CONFIRM-123"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/payment-methods-option":
			payload := `{"status":"SUCCESS","message":"","data":{"token_payment":"TOK-PAY-123","payment_for":"BUY_PACKAGE","payment_method":"BALANCE","price":25000,"timestamp":1700000000}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/settlement-multipayment":
			payload := `{"status":"SUCCESS","message":"","data":{"transaction_code":"TRX-BAL-OK"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		default:
			http.NotFound(w, r)
		}
	}))

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	cfg.BaseAPIURL = server.URL

	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)
	plugin := New(repo, client)

	stateStore := callback.NewStateStore()
	plugin.SetStateStore(stateStore)

	return plugin, server, repo
}

func TestMenuManager_Screens(t *testing.T) {
	plugin, server, repo := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()

	// 1. Dashboard without active account
	screen, err := plugin.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildDashboardScreen failed: %v", err)
	}
	if !strings.Contains(screen.Body, "Belum ada akun") {
		t.Errorf("expected 'Belum ada akun' screen, got: %s", screen.Body)
	}

	// Add an account
	now := time.Now()
	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "acc_tok",
		RefreshToken:   "ref_tok",
		TokenExpiresAt: now.Add(time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatalf("save account failed: %v", err)
	}

	// 2. Dashboard with active account (masked vs unmasked)
	screenUnmasked, err := plugin.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildDashboardScreen failed: %v", err)
	}
	if !strings.Contains(screenUnmasked.Body, "6281987654321") {
		t.Errorf("expected unmasked msisdn in private, got: %s", screenUnmasked.Body)
	}

	screenMasked, err := plugin.menuMgr.BuildDashboardScreen(ctx, true)
	if err != nil {
		t.Fatalf("BuildDashboardScreen failed: %v", err)
	}
	if strings.Contains(screenMasked.Body, "6281987654321") || !strings.Contains(screenMasked.Body, "6281****4321") {
		t.Errorf("expected masked msisdn in group, got: %s", screenMasked.Body)
	}

	// 3. Quota Detail screen
	quotaScreen, err := plugin.menuMgr.BuildQuotaDetailScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildQuotaDetailScreen failed: %v", err)
	}
	if !strings.Contains(quotaScreen.Body, "Akrab") {
		t.Errorf("expected quota name Akrab, got: %s", quotaScreen.Body)
	}

	// 4. Accounts screen
	accScreen, err := plugin.menuMgr.BuildAccountsScreen(ctx)
	if err != nil {
		t.Fatalf("BuildAccountsScreen failed: %v", err)
	}
	if !strings.Contains(accScreen.Body, "6281987654321") {
		t.Errorf("expected account msisdn in accounts screen, got: %s", accScreen.Body)
	}

	// 5. Store screen
	storeScreen, err := plugin.menuMgr.BuildStoreScreen(ctx)
	if err != nil {
		t.Fatalf("BuildStoreScreen failed: %v", err)
	}
	if !strings.Contains(storeScreen.Body, "Beli Paket") {
		t.Errorf("expected Beli Paket in store screen, got: %s", storeScreen.Body)
	}

	// 6. Saved packages (empty then populated)
	savedEmpty, err := plugin.menuMgr.BuildSavedPackagesScreen(ctx)
	if err != nil {
		t.Fatalf("BuildSavedPackagesScreen failed: %v", err)
	}
	if !strings.Contains(savedEmpty.Body, "Belum ada paket") {
		t.Errorf("expected empty saved packages, got: %s", savedEmpty.Body)
	}

	_ = repo.SavePackage(ctx, &SavedPackage{
		MSISDN:     acc.MSISDN,
		OptionCode: "OPT-SAVED-1",
		Name:       "Paket Hemat 50GB",
		Price:      50000,
		FamilyCode: "FAM-1",
	})

	savedPopulated, err := plugin.menuMgr.BuildSavedPackagesScreen(ctx)
	if err != nil {
		t.Fatalf("BuildSavedPackagesScreen populated failed: %v", err)
	}
	if !strings.Contains(savedPopulated.Body, "Paket Hemat 50GB") {
		t.Errorf("expected saved package name in list, got: %s", savedPopulated.Body)
	}

	// 7. Package Detail screen
	detailScreen, err := plugin.menuMgr.BuildPackageDetailScreen(ctx, acc, "OPT-10GB")
	if err != nil {
		t.Fatalf("BuildPackageDetailScreen failed: %v", err)
	}
	if !strings.Contains(detailScreen.Body, "Combo 10GB") {
		t.Errorf("expected package Combo 10GB in detail, got: %s", detailScreen.Body)
	}

	// 8. Checkout screen
	draft := purchaseDraftState{
		MSISDN:            acc.MSISDN,
		OptionCode:        "OPT-10GB",
		PackageName:       "Combo 10GB",
		Price:             25000,
		TokenConfirmation: "TOK-CONFIRM-123",
		Method:            "BALANCE",
		WalletNumber:      acc.MSISDN,
	}
	checkoutScreen, err := plugin.menuMgr.BuildCheckoutScreen(draft, 12345, 100)
	if err != nil || checkoutScreen == nil || !strings.Contains(checkoutScreen.Body, "Konfirmasi Pembelian") {
		t.Errorf("expected checkout screen confirmation")
	}

	// 9. Result screen
	resScreen := plugin.menuMgr.BuildPurchaseResultScreen(&SettlementResult{
		IsSuccess:       true,
		TransactionCode: "TRX-TEST-OK",
	}, "Combo 10GB", 25000, "BALANCE", "OPT-10GB")
	if !strings.Contains(resScreen.Body, "Pembelian Berhasil") {
		t.Errorf("expected purchase success screen, got: %s", resScreen.Body)
	}

	// 9b. Result screen with QRIS
	qrisPayload := "00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6"
	qrisScreen := plugin.menuMgr.BuildPurchaseResultScreen(&SettlementResult{
		IsSuccess:       true,
		TransactionCode: "TRX-QRIS-OK",
		QRCode:          qrisPayload,
	}, "Combo QRIS 10GB", 25000, "QRIS", "OPT-QRIS")
	if !strings.Contains(qrisScreen.Body, "Kode / String QRIS") || !strings.Contains(qrisScreen.Body, "Foto QRIS dikirimkan") {
		t.Errorf("expected QRIS screen to contain QRIS info and photo notice, got: %s", qrisScreen.Body)
	}
	hasPhotoBtn := false
	for _, row := range qrisScreen.Rows {
		for _, b := range row {
			if strings.Contains(b.Text, "Kirim Foto QRIS") {
				hasPhotoBtn = true
			}
		}
	}
	if !hasPhotoBtn {
		t.Errorf("expected Kirim Foto QRIS button in qrisScreen")
	}

	// 10. Delete pick & Alias pick screens
	delScreen, err := plugin.menuMgr.BuildDeletePickScreen(ctx)
	if err != nil || !strings.Contains(delScreen.Body, "Hapus Akun") {
		t.Errorf("expected delete pick screen")
	}

	aliasScreen, err := plugin.menuMgr.BuildAliasPickScreen(ctx)
	if err != nil || !strings.Contains(aliasScreen.Body, "Ubah Alias") {
		t.Errorf("expected alias pick screen")
	}

	// 11. Family packages screen (page 1 and page 2)
	famScreen1, err := plugin.menuMgr.BuildFamilyPackagesScreen(ctx, acc, "FAM-FLEX", 1)
	if err != nil {
		t.Fatalf("BuildFamilyPackagesScreen page 1 failed: %v", err)
	}
	if !strings.Contains(famScreen1.Body, "Paket Xtra Combo Flex") || !strings.Contains(famScreen1.Body, "1 dari 2") {
		t.Errorf("unexpected famScreen1 body: %s", famScreen1.Body)
	}
	if !strings.Contains(famScreen1.Body, "[1] Flex S 10GB") || !strings.Contains(famScreen1.Body, "[5] Flex XXL 80GB") {
		t.Errorf("expected items 1-5 on page 1, got: %s", famScreen1.Body)
	}

	famScreen2, err := plugin.menuMgr.BuildFamilyPackagesScreen(ctx, acc, "FAM-FLEX", 2)
	if err != nil {
		t.Fatalf("BuildFamilyPackagesScreen page 2 failed: %v", err)
	}
	if !strings.Contains(famScreen2.Body, "2 dari 2") || !strings.Contains(famScreen2.Body, "[6] Flex Max 100GB") {
		t.Errorf("expected items 6-7 on page 2, got: %s", famScreen2.Body)
	}
}

func TestMenuManager_LongOptionCodeRemainsSessionOwned(t *testing.T) {
	plugin, server, _ := setupTestMyXLEnv(t)
	defer server.Close()

	code := "7658c955-a0b9-405f-bb17-de7f43d1a946:OPTION-LONG"
	key := plugin.menuMgr.RegisterOptionCode(code)
	if key != code {
		t.Fatalf("RegisterOptionCode(%q) = %q, want raw session-owned value", code, key)
	}
	if got := plugin.menuMgr.ResolveOptionCode(key); got != code {
		t.Fatalf("ResolveOptionCode(%q) = %q, want %q", key, got, code)
	}
}

func TestMenuManager_PendingQRISScreen(t *testing.T) {
	plugin, server, repo := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()

	// 1. No active account
	screen, err := plugin.menuMgr.BuildPendingQRISScreen(ctx)
	if err == nil || screen != nil {
		t.Fatalf("expected error for no active account, got screen: %v, err: %v", screen, err)
	}

	// 2. Active account, but no pending QRIS
	now := time.Now().UTC()
	acc := &Account{
		MSISDN:         "6281987654321",
		IsActive:       true,
		AccessToken:    "acc_tok",
		RefreshToken:   "ref_tok",
		TokenExpiresAt: now.Add(time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatalf("save account failed: %v", err)
	}

	screen, err = plugin.menuMgr.BuildPendingQRISScreen(ctx)
	if err != nil {
		t.Fatalf("BuildPendingQRISScreen failed: %v", err)
	}
	if !strings.Contains(screen.Body, "Tidak ada transaksi QRIS aktif") {
		t.Errorf("expected empty pending QRIS message, got: %s", screen.Body)
	}

	// Dashboard should not have QRIS banner
	dash, err := plugin.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildDashboardScreen failed: %v", err)
	}
	if strings.Contains(dash.Body, "Tagihan QRIS Menunggu Pembayaran") {
		t.Errorf("expected no QRIS banner on dashboard when none pending")
	}

	// 3. Save an active pending QRIS (valid for 5 minutes)
	pending := &PendingQRIS{
		TransactionCode: "TRX-QRIS-999",
		IdempotencyKey:  "idemp-key-1",
		MSISDN:          acc.MSISDN,
		OptionCode:      "OPT-COMBO",
		PackageName:     "Combo VIP 50GB",
		Price:           50000,
		QRCode:          "00020101021226570011ID.CO.QRIS.WWW...",
		Status:          "PENDING",
		CreatedAt:       now,
		ExpiresAt:       now.Add(5 * time.Minute),
	}
	if err := repo.SavePendingQRIS(ctx, pending); err != nil {
		t.Fatalf("SavePendingQRIS failed: %v", err)
	}

	// BuildPendingQRISScreen with active pending QRIS
	screen, err = plugin.menuMgr.BuildPendingQRISScreen(ctx)
	if err != nil {
		t.Fatalf("BuildPendingQRISScreen failed: %v", err)
	}
	if !strings.Contains(screen.Body, "Combo VIP 50GB") || !strings.Contains(screen.Body, "TRX-QRIS-999") {
		t.Errorf("expected package name and tx code in screen, got: %s", screen.Body)
	}
	if !strings.Contains(screen.Body, "00020101021226570011ID.CO.QRIS.WWW...") {
		t.Errorf("expected raw QRIS string in screen, got: %s", screen.Body)
	}

	// Dashboard should now show banner
	dash, err = plugin.menuMgr.BuildDashboardScreen(ctx, false)
	if err != nil {
		t.Fatalf("BuildDashboardScreen failed: %v", err)
	}
	if !strings.Contains(dash.Body, "Tagihan QRIS Menunggu Pembayaran") {
		t.Errorf("expected QRIS banner on dashboard")
	}

	// 4. Test expired pending QRIS (> 5 minutes old)
	expiredPending := &PendingQRIS{
		TransactionCode: "TRX-EXPIRED",
		IdempotencyKey:  "idemp-expired",
		MSISDN:          acc.MSISDN,
		OptionCode:      "OPT-OLD",
		PackageName:     "Paket Lama",
		Price:           10000,
		QRCode:          "00020101...",
		Status:          "PENDING",
		CreatedAt:       now.Add(-10 * time.Minute),
		ExpiresAt:       now.Add(-5 * time.Minute),
	}
	_ = repo.DeletePendingQRIS(ctx, pending.TransactionCode)
	if err := repo.SavePendingQRIS(ctx, expiredPending); err != nil {
		t.Fatalf("SavePendingQRIS expired failed: %v", err)
	}

	screen, err = plugin.menuMgr.BuildPendingQRISScreen(ctx)
	if err != nil {
		t.Fatalf("BuildPendingQRISScreen failed: %v", err)
	}
	if !strings.Contains(screen.Body, "Tidak ada transaksi QRIS aktif") {
		t.Errorf("expected empty pending QRIS screen for expired item, got: %s", screen.Body)
	}
}
