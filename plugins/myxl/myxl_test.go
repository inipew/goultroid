package myxl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/services/callback"
)

type mockTgService struct {
	core.MockTelegramServicer
	sent       string
	lastMarkup tg.ReplyMarkupClass
}

func (m *mockTgService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockTgService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.sent = text
	return nil
}

func (m *mockTgService) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.sent = text
	m.lastMarkup = markup
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockTgService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.sent = text
	m.lastMarkup = markup
	return nil
}

func TestMyXLPlugin_Commands(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	repo := NewSQLiteRepository(db)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/xl-ciam/auth/otp":
			_ = json.NewEncoder(w).Encode(map[string]any{"subscriber_id": "SUB-12345"})
		case "/realms/xl-ciam/protocol/openid-connect/token":
			_ = json.NewEncoder(w).Encode(Tokens{
				AccessToken:  "mock_acc_token",
				IDToken:      "mock_id_token",
				RefreshToken: "mock_ref_token",
			})
		case "/api/v8/packages/balance-and-credit":
			payload := `{"status":"SUCCESS","message":"","data":{"balance":{"remaining":75000,"expired_at":1735689600}}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/packages/quota-details":
			payload := `{"status":"SUCCESS","message":"","data":{"quotas":[{"name":"Xtra Combo","expired_at":1735689600,"benefits":[{"name":"Utama","data_type":"DATA","remaining":5368709120,"total":10737418240}]}]}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/xl-stores/options/list":
			payload := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Xtra Combo Flex","package_family_code":"FAM-FLEX"},"package_variants":[{"name":"Flex S","package_variant_code":"VAR-S","package_options":[{"name":"Flex S 10GB","package_option_code":"OPT-FLEX-S","price":35000}]}]}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/api/v8/xl-stores/options/detail":
			payload := `{"status":"SUCCESS","message":"","data":{"package_family":{"name":"Xtra Combo Flex","package_family_code":"FAM-FLEX"},"package_option":{"name":"Flex S 10GB","package_option_code":"OPT-FLEX-S","price":35000},"token_confirmation":"CONFIRM-TOKEN-123"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/payment-methods-option":
			payload := `{"status":"SUCCESS","message":"","data":{"token_payment":"TOK-PAY-999","payment_for":"BUY_PACKAGE","payment_method":"BALANCE","price":35000,"timestamp":1700000000}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/settlement-multipayment":
			payload := `{"status":"SUCCESS","message":"","data":{"transaction_code":"TRX-BAL-123"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/settlement-multipayment/qris":
			payload := `{"status":"SUCCESS","message":"","data":{"transaction_code":"TRX-QR-456"}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		case "/payments/api/v8/pending-detail":
			payload := `{"status":"SUCCESS","message":"","data":{"qr_code":"0002010102122659..."}}`
			xtime := time.Now().UnixMilli()
			xdata, _ := EncryptXData(payload, xtime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xdata, XTime: xtime})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseCIAMURL = server.URL
	cfg.BaseAPIURL = server.URL

	netCli := network.NewService(server.Client(), nil).ForOwner("myxl")
	client := NewClient(cfg, repo, netCli)
	plugin := New(repo, client)
	plugin.SetStateStore(callback.NewStateStore())

	if plugin.Name() != "myxl" {
		t.Errorf("expected name myxl, got %s", plugin.Name())
	}

	cmds := plugin.Commands()
	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	svc := &mockTgService{}
	baseCtx := &core.Context{
		Ctx:     ctx,
		Sender:  &core.User{ID: 1001},
		Chat:    &core.Chat{ID: 1001},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerUser{UserID: 1001},
	}

	// 1. .myxl menu
	ctxMenu := *baseCtx
	ctxMenu.Args = []string{}
	_ = cmdMap["myxl"].Handler(&ctxMenu)
	if !strings.Contains(svc.sent, "MyXL Plugin Menu") {
		t.Errorf("expected menu help, got %s", svc.sent)
	}

	// 2. .myxl accounts (initially empty)
	ctxAcc := *baseCtx
	ctxAcc.Args = []string{"accounts"}
	_ = cmdMap["myxl"].Handler(&ctxAcc)
	if !strings.Contains(svc.sent, "Belum ada akun") {
		t.Errorf("expected no accounts message, got %s", svc.sent)
	}

	// 3. .myxl login 081912345678
	ctxLogin := *baseCtx
	ctxLogin.Args = []string{"login", "081912345678"}
	_ = cmdMap["myxl"].Handler(&ctxLogin)
	if !strings.Contains(svc.sent, "Kode OTP telah dikirimkan") {
		t.Errorf("expected OTP sent message, got %s", svc.sent)
	}

	// 4. .myxl otp 081912345678 123456
	ctxOTP := *baseCtx
	ctxOTP.Args = []string{"otp", "081912345678", "123456"}
	_ = cmdMap["myxl"].Handler(&ctxOTP)
	if !strings.Contains(svc.sent, "Login Berhasil") {
		t.Errorf("expected login successful message, got %s", svc.sent)
	}

	// 5. Group chat operation: allowed in group chats without blocker
	ctxGroup := *baseCtx
	ctxGroup.Chat = &core.Chat{ID: -100123456, Type: "supergroup"}
	ctxGroup.Args = []string{"status"}
	_ = cmdMap["myxl"].Handler(&ctxGroup)
	if !strings.Contains(svc.sent, "6281****5678") {
		t.Errorf("expected status execution in group with masked MSISDN, got %s", svc.sent)
	}

	// 6. .myxl alias 081912345678 ModemHome
	ctxAlias := *baseCtx
	ctxAlias.Args = []string{"alias", "081912345678", "ModemHome"}
	_ = cmdMap["myxl"].Handler(&ctxAlias)
	if !strings.Contains(svc.sent, "ModemHome") {
		t.Errorf("expected alias set confirmation, got %s", svc.sent)
	}

	// 7. .myxl status
	ctxStatus := *baseCtx
	ctxStatus.Args = []string{"status"}
	_ = cmdMap["myxl"].Handler(&ctxStatus)
	if !strings.Contains(svc.sent, "ModemHome") || !strings.Contains(svc.sent, "Terautentikasi") {
		t.Errorf("expected status output with alias, got %s", svc.sent)
	}

	// 8. .myxl refresh
	ctxRefresh := *baseCtx
	ctxRefresh.Args = []string{"refresh"}
	_ = cmdMap["myxl"].Handler(&ctxRefresh)
	if !strings.Contains(svc.sent, "Token CIAM Berhasil Diperbarui") {
		t.Errorf("expected refreshed token confirmation, got %s", svc.sent)
	}

	// 9. .myxl family FAM-FLEX
	ctxFamily := *baseCtx
	ctxFamily.Args = []string{"family", "FAM-FLEX"}
	_ = cmdMap["myxl"].Handler(&ctxFamily)
	if !strings.Contains(svc.sent, "Xtra Combo Flex") || !strings.Contains(svc.sent, "OPT-FLEX-S") {
		t.Errorf("expected family packages output, got %s", svc.sent)
	}

	// 10. .myxl paket OPT-FLEX-S
	ctxPaket := *baseCtx
	ctxPaket.Args = []string{"paket", "OPT-FLEX-S"}
	_ = cmdMap["myxl"].Handler(&ctxPaket)
	if !strings.Contains(svc.sent, "Flex S 10GB") || !strings.Contains(svc.sent, "35.000") {
		t.Errorf("expected package detail output, got %s", svc.sent)
	}

	// 11. .myxl saved add OPT-FLEX-S
	ctxSave := *baseCtx
	ctxSave.Args = []string{"saved", "add", "OPT-FLEX-S"}
	_ = cmdMap["myxl"].Handler(&ctxSave)
	if !strings.Contains(svc.sent, "berhasil disimpan ke bookmark") {
		t.Errorf("expected saved confirmation, got %s", svc.sent)
	}

	// 12. .myxl saved list
	ctxSavedList := *baseCtx
	ctxSavedList.Args = []string{"saved", "list"}
	_ = cmdMap["myxl"].Handler(&ctxSavedList)
	if !strings.Contains(svc.sent, "Flex S 10GB") {
		t.Errorf("expected saved package listed, got %s", svc.sent)
	}

	// 13. .myxl buy OPT-FLEX-S pulsa 0
	ctxBuy := *baseCtx
	ctxBuy.Args = []string{"buy", "OPT-FLEX-S", "pulsa", "0"}
	_ = cmdMap["myxl"].Handler(&ctxBuy)
	if !strings.Contains(svc.sent, "Konfirmasi Pembelian") {
		t.Errorf("expected purchase confirmation, got %s", svc.sent)
	}
	_, purchaseEntry := callbackEntryFromMarkup(t, plugin.stateStore, svc.lastMarkup)
	if !purchaseEntry.Scope.SingleUse || purchaseEntry.Scope.UserID != 1001 || purchaseEntry.Scope.ChatID != 1001 {
		t.Fatalf("purchase confirmation is not single-use and scoped: %#v", purchaseEntry.Scope)
	}
	_ = plugin.HandleCallback(&callback.CallbackContext{
		Ctx: ctx, Action: "buy_confirm", UserID: 1001, Service: svc,
		Target: core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 1001}, MessageID: 10},
		State: purchaseDraftState{
			MSISDN: "6281912345678", OptionCode: "OPT-FLEX-S", PackageName: "Flex S 10GB",
			Price: 35000, TokenConfirmation: "CONFIRM-TOKEN-123", Method: "balance",
			HasOverwrite: true, OverwritePrice: 0,
		},
	})
	if !strings.Contains(svc.sent, "TRX-BAL-123") {
		t.Errorf("expected balance purchase transaction code, got %s", svc.sent)
	}

	// 14. .beli OPT-FLEX-S qris 1000
	ctxBeli := *baseCtx
	ctxBeli.Args = []string{"OPT-FLEX-Q", "qris", "1000"}
	_ = cmdMap["beli"].Handler(&ctxBeli)
	_ = plugin.HandleCallback(&callback.CallbackContext{
		Ctx: ctx, Action: "buy_confirm", UserID: 1001, Service: svc,
		Target: core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 1001}, MessageID: 10},
		State: purchaseDraftState{
			MSISDN: "6281912345678", OptionCode: "OPT-FLEX-Q", PackageName: "Flex Q",
			Price: 35000, TokenConfirmation: "CONFIRM-TOKEN-123", Method: "qris",
			HasOverwrite: true, OverwritePrice: 1000,
		},
	})
	if !strings.Contains(svc.sent, "TRX-QR-456") || !strings.Contains(svc.sent, "0002010102122659...") {
		t.Errorf("expected QRIS transaction code and QR string, got %s", svc.sent)
	}

	// 15. .kuota (shortcut)
	ctxKuota := *baseCtx
	ctxKuota.Args = []string{}
	_ = cmdMap["kuota"].Handler(&ctxKuota)
	if !strings.Contains(svc.sent, "Rp 75.000") {
		t.Errorf("expected balance Rp 75.000, got %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Xtra Combo") {
		t.Errorf("expected package Xtra Combo, got %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "5.00 GB / 10.00 GB") {
		t.Errorf("expected quota 5.00 GB / 10.00 GB, got %s", svc.sent)
	}

	// 16. Test Capabilities declaration
	caps := plugin.Capabilities()
	if len(caps) == 0 || caps[0].ID != "myxl" {
		t.Errorf("expected capability myxl, got %#v", caps)
	}

	// 17. Test Callback handler
	cbCtx := &callback.CallbackContext{
		Ctx:       ctx,
		Action:    "refresh",
		State:     quotaRefreshState{MSISDN: "6281912345678", Masked: true},
		Target:    core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 1001}, MessageID: 1},
		Namespace: "myxl",
		UserID:    1001,
		Service:   svc,
	}
	if err := plugin.HandleCallback(cbCtx); err != nil {
		t.Errorf("HandleCallback failed: %v", err)
	}

	// 18. .myxl del 081912345678
	ctxDel := *baseCtx
	ctxDel.Args = []string{"del", "081912345678"}
	_ = cmdMap["myxl"].Handler(&ctxDel)
	if !strings.Contains(svc.sent, "berhasil dihapus") {
		t.Errorf("expected deleted message, got %s", svc.sent)
	}
}

func TestRefreshMarkupStoresScopedMaskedState(t *testing.T) {
	store := callback.NewStateStore()
	p := &Plugin{stateStore: store}
	markup := p.buildRefreshMarkup(quotaRefreshState{MSISDN: "6281912345678", Masked: true}, callback.StateScope{
		UserID: 42, ChatID: -10099, Namespace: "myxl",
	})
	_, entry := callbackEntryFromMarkup(t, store, markup)
	state, ok := entry.Data.(quotaRefreshState)
	if !ok || !state.Masked || state.MSISDN != "6281912345678" {
		t.Fatalf("unexpected refresh state: %#v", entry.Data)
	}
	if entry.Scope.UserID != 42 || entry.Scope.ChatID != -10099 || entry.Scope.Namespace != "myxl" {
		t.Fatalf("unexpected callback scope: %#v", entry.Scope)
	}
}

func callbackEntryFromMarkup(t *testing.T, store *callback.StateStore, markup tg.ReplyMarkupClass) (string, callback.StateEntry) {
	t.Helper()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) == 0 || len(inline.Rows[0].Buttons) == 0 {
		t.Fatalf("unexpected refresh markup: %#v", markup)
	}
	button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("unexpected button type: %T", inline.Rows[0].Buttons[0])
	}
	_, _, oid, err := callback.ParseCallbackData(button.Data)
	if err != nil {
		t.Fatalf("parse callback data: %v", err)
	}
	entry, err := store.GetEntry(oid)
	if err != nil {
		t.Fatalf("get callback state: %v", err)
	}
	return oid, entry
}
