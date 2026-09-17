package myxl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/menu"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/services/callback"
)

type mockAssistantMessenger struct {
	sentText   string
	lastMarkup tg.ReplyMarkupClass
	editTarget interaction.MessageTarget
}

func (m *mockAssistantMessenger) SendTextMessage(ctx context.Context, target interaction.MessageTarget, text string) error {
	m.sentText = text
	m.editTarget = target
	return nil
}

func (m *mockAssistantMessenger) SendScreen(ctx context.Context, target interaction.MessageTarget, screen *menu.Screen) error {
	return nil
}

func (m *mockAssistantMessenger) EditScreen(ctx context.Context, target interaction.MessageTarget, screen *menu.Screen) error {
	m.editTarget = target
	return nil
}

func (m *mockAssistantMessenger) DeleteMessage(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}

func (m *mockAssistantMessenger) SendToast(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}

type mockInteraction struct {
	sentText string
}

func (m *mockInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}

func (m *mockInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	m.sentText = text
	return nil
}

func (m *mockInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}

func (m *mockInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}

func (m *mockInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}

func (m *mockInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 11, Message: caption}, nil
}

func setupTestMyXLEnv(t *testing.T) (*Plugin, *httptest.Server, *SQLiteRepository, *menu.Controller) {
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

	menuCtrl := menu.NewController(func(s *menu.Screen) (string, tg.ReplyMarkupClass) {
		return s.Body, nil
	})
	plugin.SetAssistantMenu(menuCtrl)

	return plugin, server, repo, menuCtrl
}

func TestMenuManager_Screens(t *testing.T) {
	plugin, server, repo, _ := setupTestMyXLEnv(t)
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

func TestMenuManager_Wizards(t *testing.T) {
	plugin, server, repo, _ := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()
	inter := &mockInteraction{}
	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 12345}, 200, 100, 0)
	userID := int64(12345)
	chatID := int64(100)

	// 1. Cancel wizard
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:   wizardLoginMSISDN,
		Target: target,
	})
	handled, err := plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "/cancel", inter)
	if !handled || err != nil {
		t.Fatalf("expected /cancel to be handled cleanly: %v", err)
	}
	if _, ok := plugin.menuMgr.GetSession(userID); ok {
		t.Errorf("expected wizard to be nil after cancel")
	}

	// 2. Login Wizard full flow
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:   wizardLoginMSISDN,
		Target: target,
	})
	// Invalid phone
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "hello", inter)
	if !handled || err != nil {
		t.Fatalf("expected text handled for invalid phone: %v", err)
	}
	sess, ok := plugin.menuMgr.GetSession(userID)
	if !ok || sess == nil || sess.Type != wizardLoginMSISDN {
		t.Fatalf("expected still in loginMSISDN step")
	}

	// Valid phone
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "081987654321", inter)
	if !handled || err != nil {
		t.Fatalf("expected valid phone handled: %v", err)
	}
	sess, ok = plugin.menuMgr.GetSession(userID)
	if !ok || sess == nil || sess.Type != wizardLoginOTP {
		t.Fatalf("expected transition to loginOTP step")
	}

	// Submit OTP
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "123456", inter)
	if !handled || err != nil {
		t.Fatalf("expected OTP handled: %v", err)
	}
	if _, ok = plugin.menuMgr.GetSession(userID); ok {
		t.Errorf("expected wizard cleared after successful OTP")
	}
	acc, err := repo.GetByMSISDN(ctx, "6281987654321")
	if err != nil || acc == nil {
		t.Fatalf("expected account 6281987654321 saved in repository")
	}

	// 3. Alias Wizard
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:   wizardSetAlias,
		MSISDN: "6281987654321",
		Target: target,
	})
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "RouterUtama", inter)
	if !handled || err != nil {
		t.Fatalf("expected alias handled: %v", err)
	}
	acc, _ = repo.GetByMSISDN(ctx, "6281987654321")
	if acc.Alias != "RouterUtama" {
		t.Errorf("expected alias RouterUtama, got: %s", acc.Alias)
	}

	// 4. Option Code Wizard
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:   wizardOptionCode,
		Target: target,
	})
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "OPT-10GB", inter)
	if !handled || err != nil {
		t.Fatalf("expected option code handled: %v", err)
	}
	if _, ok = plugin.menuMgr.GetSession(userID); ok {
		t.Errorf("expected wizard cleared after option code lookup")
	}

	// 5. Custom Price Wizard
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:        wizardCustomPrice,
		OptionCode:  "OPT-10GB",
		PackageName: "Combo 10GB",
		Price:       25000,
		Method:      "BALANCE",
		Target:      target,
	})
	// Invalid numeric price
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "free", inter)
	if !handled || err != nil {
		t.Fatalf("expected invalid price handled: %v", err)
	}
	sess, ok = plugin.menuMgr.GetSession(userID)
	if !ok || sess == nil || sess.Type != wizardCustomPrice {
		t.Fatalf("expected still in custom price wizard")
	}
	// Valid numeric price
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "20000", inter)
	if !handled || err != nil {
		t.Fatalf("expected valid price handled: %v", err)
	}
	if _, ok = plugin.menuMgr.GetSession(userID); ok {
		t.Errorf("expected custom price wizard completed")
	}

	// 6. Family Code Wizard
	plugin.menuMgr.SetSession(userID, &wizardSession{
		Type:   wizardFamilyCode,
		Target: target,
	})
	handled, err = plugin.menuMgr.HandleTextMessage(ctx, userID, chatID, "FAM-FLEX", inter)
	if !handled || err != nil {
		t.Fatalf("expected family code handled: %v", err)
	}
	if _, ok = plugin.menuMgr.GetSession(userID); ok {
		t.Errorf("expected family code wizard cleared after lookup")
	}
}

func TestMenuManager_Callbacks(t *testing.T) {
	plugin, server, repo, menuCtrl := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()
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

	menuCtrl.RegisterInstance(menu.MenuInstance{
		ID:        "menu:100:200",
		ChatID:    100,
		MessageID: 200,
		Screen:    menu.ScreenIDMyXL,
		OwnerID:   12345,
	})

	type testCase struct {
		name     string
		action   string
		opaqueID string
		state    any
		senderID int64
		wantErr  bool
	}

	optKey := plugin.menuMgr.RegisterOptionCode("OPT-10GB")
	qrKey := plugin.menuMgr.RegisterQR("00020101021226570011ID.CO.QRIS.WWW01189360002140000000001030301UBE51440014ID.LINKAJA.WWW0215ID20201770500670303UBE52040000530336054031005802ID5921TEST MERCHANT QRIS6013JAKARTA PUSAT610512345630421A6")

	cases := []testCase{
		{name: "Unauthorized user", action: "home", senderID: 99999, wantErr: false},
		{name: "Home", action: "home", senderID: 12345, wantErr: false},
		{name: "Refresh", action: "refresh", senderID: 12345, wantErr: false},
		{name: "Detail", action: "detail", senderID: 12345, wantErr: false},
		{name: "Quota alias", action: "quota", senderID: 12345, wantErr: false},
		{name: "Accounts", action: "accounts", senderID: 12345, wantErr: false},
		{name: "Store", action: "store", senderID: 12345, wantErr: false},
		{name: "Saved", action: "saved", senderID: 12345, wantErr: false},
		{name: "Token Refresh", action: "token_refresh", senderID: 12345, wantErr: false},
		{name: "Delete Pick", action: "del_pick", senderID: 12345, wantErr: false},
		{name: "Delete Ask", action: "del_ask", opaqueID: "6281987654321", senderID: 12345, wantErr: false},
		{name: "Delete Exec", action: "del_exec", opaqueID: "6281987654321", senderID: 12345, wantErr: false},
		{name: "Alias Pick", action: "alias_pick", senderID: 12345, wantErr: false},
		{name: "Alias Req", action: "alias_req", opaqueID: "6281987654321", senderID: 12345, wantErr: false},
		{name: "Login Req", action: "login_req", senderID: 12345, wantErr: false},
		{name: "Resend OTP", action: "resend_otp", opaqueID: "6281987654321", senderID: 12345, wantErr: false},
		{name: "Cancel Wizard", action: "cancel_wizard", senderID: 12345, wantErr: false},
		{name: "Switch Account", action: "switch", opaqueID: "6281987654321", senderID: 12345, wantErr: false},
		{name: "Switch Noop", action: "switch", opaqueID: "noop", senderID: 12345, wantErr: false},
		{name: "Family Input", action: "fam_input", senderID: 12345, wantErr: false},
		{name: "Family Page", action: "fam_page", opaqueID: "FAM-FLEX:1", senderID: 12345, wantErr: false},
		{name: "Family Page UUID", action: "fam_page", opaqueID: "7658c955-a0b9-405f-bb17-de7f43d1a946:1", senderID: 12345, wantErr: false},
		{name: "Buy Option Input", action: "buy_opt_input", senderID: 12345, wantErr: false},
		{name: "Buy Option Plain", action: "buy_opt", opaqueID: "OPT-10GB", senderID: 12345, wantErr: false},
		{name: "Buy Option Mapped Key", action: "buy_opt", opaqueID: optKey, senderID: 12345, wantErr: false},
		{name: "Method Balance", action: "method", opaqueID: "balance:OPT-10GB", senderID: 12345, wantErr: false},
		{name: "Method Mapped Key", action: "method", opaqueID: "balance:" + optKey, senderID: 12345, wantErr: false},
		{name: "Custom Price", action: "custom_price", opaqueID: optKey, senderID: 12345, wantErr: false},
		{
			name:   "Checkout with valid draft",
			action: "checkout",
			state: purchaseDraftState{
				MSISDN:            "6281987654321",
				OptionCode:        "OPT-10GB",
				PackageName:       "Combo 10GB",
				Price:             25000,
				TokenConfirmation: "TOK-CONFIRM-123",
				Method:            "balance",
			},
			senderID: 12345,
			wantErr:  false,
		},
		{name: "Cancel Draft", action: "cancel_draft", senderID: 12345, wantErr: false},
		{name: "Bookmark Add", action: "bookmark_add", opaqueID: "OPT-10GB", senderID: 12345, wantErr: false},
		{name: "Bookmark Add Mapped", action: "bookmark_add", opaqueID: optKey, senderID: 12345, wantErr: false},
		{name: "Bookmark Del", action: "bookmark_del", opaqueID: "OPT-10GB", senderID: 12345, wantErr: false},
		{name: "QRIS Image Send", action: "qris_img", opaqueID: qrKey, senderID: 12345, wantErr: false},
		{name: "QRIS Image Missing", action: "qris_img", opaqueID: "missing_qr", senderID: 12345, wantErr: false},
		{name: "Pending QRIS", action: "pending_qris", senderID: 12345, wantErr: false},
		{name: "QRIS Cancel", action: "qris_cancel", opaqueID: "TRX-12345", senderID: 12345, wantErr: false},
		{name: "Noop", action: "noop", senderID: 12345, wantErr: false},
		{name: "Unknown Action", action: "unknown_action_xyz", senderID: 12345, wantErr: false},
	}

	svc := &mockTgService{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = repo.Save(ctx, acc)
			_ = repo.SetActive(ctx, acc.MSISDN)

			action := tc.action
			opaqueID := tc.opaqueID
			if opaqueID == "" && strings.Contains(action, ":") {
				parts := strings.SplitN(action, ":", 2)
				action = parts[0]
				opaqueID = parts[1]
			}

			rawData := []byte(fmt.Sprintf("a1:myxl:%s", action))
			if opaqueID != "" {
				rawData = []byte(fmt.Sprintf("a1:myxl:%s:%s", action, opaqueID))
			}
			cbCtx := &callback.CallbackContext{
				Ctx:      ctx,
				QueryID:  1001,
				UserID:   tc.senderID,
				ChatID:   100,
				MsgID:    200,
				Action:   action,
				OpaqueID: opaqueID,
				RawData:  rawData,
				State:    tc.state,
				Service:  svc,
				Target: core.CallbackTarget{
					Peer:      &tg.InputPeerChat{ChatID: 100},
					MessageID: 200,
				},
			}
			err := plugin.HandleCallback(cbCtx)
			if (err != nil) != tc.wantErr {
				t.Errorf("HandleCallback(%s, %s) error = %v, wantErr %v", action, opaqueID, err, tc.wantErr)
			}
		})
	}
}

func TestMenuManager_RejectsCallbackFromNonOwner(t *testing.T) {
	plugin, server, repo, menuCtrl := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()
	acc := &Account{MSISDN: "6281987654321", IsActive: true}
	if err := repo.Save(ctx, acc); err != nil {
		t.Fatal(err)
	}
	menuCtrl.RegisterInstance(menu.MenuInstance{
		ChatID: 100, MessageID: 200, OwnerID: 12345, Screen: menu.ScreenIDMyXL,
	})

	err := plugin.HandleCallback(&callback.CallbackContext{
		Ctx: ctx, QueryID: 1, UserID: 99999, ChatID: 100,
		Action: "del_exec", OpaqueID: acc.MSISDN, Service: &mockTgService{},
		Target: core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 100}, MessageID: 200},
	})
	if err != nil {
		t.Fatalf("rejection should be delivered as a callback answer: %v", err)
	}
	if got, _ := repo.GetByMSISDN(ctx, acc.MSISDN); got == nil {
		t.Fatal("unauthorized callback deleted the account")
	}
}

func TestMenuManager_LongOptionCodeUsesStateToken(t *testing.T) {
	plugin, server, _, _ := setupTestMyXLEnv(t)
	defer server.Close()

	code := "7658c955-a0b9-405f-bb17-de7f43d1a946:OPTION-LONG"
	key := plugin.menuMgr.RegisterOptionCode(code)
	if key == "" || key == code || len(key) > 24 {
		t.Fatalf("unexpected compact option key %q", key)
	}
	if got := plugin.menuMgr.ResolveOptionCode(key); got != code {
		t.Fatalf("ResolveOptionCode(%q) = %q, want %q", key, got, code)
	}
}

func TestMenuManager_HandleMyXLEntrypoint(t *testing.T) {
	plugin, server, repo, menuCtrl := setupTestMyXLEnv(t)
	defer server.Close()

	ctx := context.Background()
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

	svc := &mockTgService{}

	// 1. Assistant /myxl without args -> should render interactive dashboard with buttons
	asstCtx := &core.Context{
		Ctx:       ctx,
		Source:    core.ExecutionAssistant,
		Command:   "myxl",
		Args:      []string{},
		PeerID:    &tg.InputPeerUser{UserID: 12345},
		Sender:    &core.User{ID: 12345},
		Chat:      &core.Chat{ID: 12345, Type: "private"},
		Svc:       svc,
		Principal: &core.Principal{UserID: 12345, IsOwner: true},
	}
	err := plugin.handleMyXL(asstCtx)
	if err != nil {
		t.Fatalf("handleMyXL in Assistant failed: %v", err)
	}
	if svc.lastMarkup == nil {
		t.Errorf("expected inline markup buttons from /myxl in Assistant")
	}
	if !strings.Contains(svc.sent, "MyXL Control Center") {
		t.Errorf("expected dashboard title, got: %s", svc.sent)
	}
	// Verify instance registered
	inst, ok := menuCtrl.Instances().Get(12345, 10)
	if !ok || inst == nil {
		t.Errorf("expected menu instance registered for assistant interaction")
	}

	// 2. Userbot .myxl without args -> should reply with text CLI help
	userCtx := &core.Context{
		Ctx:       ctx,
		Source:    core.ExecutionInteractive,
		Command:   "myxl",
		Args:      []string{},
		PeerID:    &tg.InputPeerUser{UserID: 12345},
		Sender:    &core.User{ID: 12345},
		Chat:      &core.Chat{ID: 12345, Type: "private"},
		Svc:       svc,
		Principal: &core.Principal{UserID: 12345, IsOwner: true},
	}
	svc.lastMarkup = nil
	err = plugin.handleMyXL(userCtx)
	if err != nil {
		t.Fatalf("handleMyXL in Userbot failed: %v", err)
	}
	if svc.lastMarkup != nil {
		t.Errorf("expected no markup from plain .myxl in Userbot")
	}
	if !strings.Contains(svc.sent, "MyXL Plugin Menu") {
		t.Errorf("expected text menu help, got: %s", svc.sent)
	}

	// 3. Userbot .myxl menu -> should trigger interactive screen
	userMenuCtx := &core.Context{
		Ctx:       ctx,
		Source:    core.ExecutionInteractive,
		Command:   "myxl",
		Args:      []string{"menu"},
		PeerID:    &tg.InputPeerUser{UserID: 12345},
		Sender:    &core.User{ID: 12345},
		Chat:      &core.Chat{ID: 12345, Type: "private"},
		Svc:       svc,
		Principal: &core.Principal{UserID: 12345, IsOwner: true},
	}
	err = plugin.handleMyXL(userMenuCtx)
	if err != nil {
		t.Fatalf("handleMyXL .myxl menu failed: %v", err)
	}
	if svc.lastMarkup == nil {
		t.Errorf("expected markup from .myxl menu")
	}
}

func TestMenuManager_PendingQRISScreen(t *testing.T) {
	plugin, server, repo, _ := setupTestMyXLEnv(t)
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
