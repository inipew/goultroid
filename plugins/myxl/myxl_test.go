package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/tasks"
)

type mockTgService struct {
	core.MockTelegramServicer
	sent       string
	lastMarkup tg.ReplyMarkupClass
}

type immediateMyXLTicket struct {
	result tasks.TaskResult
	done   chan struct{}
}

func (t *immediateMyXLTicket) TaskID() tasks.TaskID {
	return t.result.TaskID
}

func (t *immediateMyXLTicket) State() tasks.TaskState {
	return tasks.StateCompleted
}

func (t *immediateMyXLTicket) Done() <-chan struct{} {
	return t.done
}

func (t *immediateMyXLTicket) Result() (tasks.TaskResult, bool) {
	return t.result, true
}

func (t *immediateMyXLTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return t.result, nil
}

type immediateMyXLTaskClient struct {
	specs []tasks.WorkSpec
}

func (c *immediateMyXLTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	recorded := spec
	recorded.Handler = nil
	recorded.Commit = nil
	recorded.OnComplete = nil
	recorded.Resources = append([]tasks.ResourceRequirement(nil), spec.Resources...)
	c.specs = append(c.specs, recorded)

	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if spec.Handler != nil {
		if err := spec.Handler(tasks.WithHeldResources(ctx, spec.Resources)); err != nil {
			result.Outcome = tasks.OutcomeFailed
			result.Failure.Message = err.Error()
		}
	}
	done := make(chan struct{})
	close(done)
	return &immediateMyXLTicket{result: result, done: done}, nil
}

func (c *immediateMyXLTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *immediateMyXLTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *immediateMyXLTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type myXLQRService struct {
	core.MockTelegramServicer
	mediaSent      bool
	heldMediaLease bool
}

func (s *myXLQRService) SendMedia(ctx context.Context, _ tg.InputPeerClass, _, _, _ string) (*tg.Message, error) {
	s.mediaSent = true
	s.heldMediaLease = tasks.HasHeldResource(ctx, "media")
	return &tg.Message{ID: 55}, nil
}

func TestPlugin_CallbackOptions_HandlerOwnsAnswer(t *testing.T) {
	if opts := (&Plugin{}).CallbackOptions(); opts.AutoAnswer {
		t.Fatal("MyXL callbacks must not be pre-answered before action-specific feedback")
	}
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

	// 6. .myxl alias 081912345678 Modem Home
	ctxAlias := *baseCtx
	ctxAlias.Args = []string{"alias", "081912345678", "Modem", "Home"}
	_ = cmdMap["myxl"].Handler(&ctxAlias)
	if !strings.Contains(svc.sent, "Modem Home") {
		t.Errorf("expected alias set confirmation, got %s", svc.sent)
	}

	// 7. .myxl status
	ctxStatus := *baseCtx
	ctxStatus.Args = []string{"status"}
	_ = cmdMap["myxl"].Handler(&ctxStatus)
	if !strings.Contains(svc.sent, "Modem Home") || !strings.Contains(svc.sent, "Terautentikasi") {
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
	purchaseData := callbackDataFromMarkup(t, svc.lastMarkup)
	purchaseStateStore, ok := plugin.stateStore.(*callback.StateStore)
	if !ok || purchaseStateStore == nil {
		t.Fatalf("purchase state writer type = %T, want *callback.StateStore", plugin.stateStore)
	}
	purchaseRouter := callback.NewRouter(nil, purchaseStateStore)
	purchaseCapture := &callbackStateCaptureHandler{}
	purchaseRegistration, err := purchaseRouter.RegisterOwned("test", purchaseCapture)
	if err != nil {
		t.Fatalf("register purchase capture handler: %v", err)
	}
	defer purchaseRegistration.Close()
	dispatchPurchaseState := func(queryID, userID, chatID int64) error {
		evt := &core.CallbackQueryEvent{QueryID: queryID, UserID: userID, ChatID: chatID, Data: purchaseData}
		prepared, err := purchaseRouter.Prepare(context.Background(), evt, svc, func(owner string) (tasks.ScopeIdentity, bool) {
			if owner != "test" {
				return tasks.ScopeIdentity{}, false
			}
			return tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}, true
		})
		if err != nil {
			return err
		}
		return prepared.Dispatch(context.Background(), evt, svc)
	}
	if err := dispatchPurchaseState(1301, 1002, 1001); !errors.Is(err, callback.ErrUnauthorized) {
		t.Fatalf("purchase state accepted wrong user: %v", err)
	}
	if err := dispatchPurchaseState(1302, 1001, 1002); !errors.Is(err, callback.ErrUnauthorized) {
		t.Fatalf("purchase state accepted wrong chat: %v", err)
	}
	if err := dispatchPurchaseState(1303, 1001, 1001); err != nil {
		t.Fatalf("purchase state canonical dispatch failed: %v", err)
	}
	capturedPurchase, ok := purchaseCapture.state.(purchaseIntentState)
	if !ok || capturedPurchase.MSISDN != "6281912345678" || capturedPurchase.OptionCode != "OPT-FLEX-S" {
		t.Fatalf("unexpected canonical purchase state: %#v", purchaseCapture.state)
	}
	if capturedPurchase.QuotedPrice != 35000 || capturedPurchase.Method != "balance" {
		t.Fatalf("unexpected purchase quote state: %#v", capturedPurchase)
	}
	if err := dispatchPurchaseState(1304, 1001, 1001); !errors.Is(err, callback.ErrStateNotFound) {
		t.Fatalf("single-use purchase state replay error = %v, want %v", err, callback.ErrStateNotFound)
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

type callbackStateCaptureHandler struct {
	state any
}

func (h *callbackStateCaptureHandler) Namespace() string { return "myxl" }
func (h *callbackStateCaptureHandler) HandleCallback(ctx *callback.CallbackContext) error {
	h.state = ctx.State
	return nil
}

func callbackDataFromMarkup(t *testing.T, markup tg.ReplyMarkupClass) []byte {
	t.Helper()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) == 0 || len(inline.Rows[0].Buttons) == 0 {
		t.Fatalf("unexpected refresh markup: %#v", markup)
	}
	button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("unexpected button type: %T", inline.Rows[0].Buttons[0])
	}
	if _, _, _, err := callback.ParseCallbackData(button.Data); err != nil {
		t.Fatalf("parse callback data: %v", err)
	}
	return append([]byte(nil), button.Data...)
}

func TestReservePurchase_ConcurrencyAndDebounce(t *testing.T) {
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
	msisdn := "6287711223344"
	opt1 := "OPT-PACKAGE-A"
	opt2 := "OPT-PACKAGE-B"

	// 1. First reservation succeeds
	key1 := "tx-token-1"
	reserved, err := repo.ReservePurchase(ctx, key1, msisdn, opt1, "balance")
	if err != nil || !reserved {
		t.Fatalf("expected first purchase to be reserved: %v", err)
	}

	// 2. Duplicate click with same key while pending must be rejected
	reserved, err = repo.ReservePurchase(ctx, key1, msisdn, opt1, "balance")
	if err != nil || reserved {
		t.Fatalf("expected duplicate key reservation to be rejected")
	}

	// 3. Concurrent purchase on same MSISDN with different key while pending must be rejected
	key2 := "tx-token-2"
	reserved, err = repo.ReservePurchase(ctx, key2, msisdn, opt2, "balance")
	if err != nil || reserved {
		t.Fatalf("expected in-flight concurrent purchase on same MSISDN to be rejected")
	}

	// 4. Finish first purchase as SUCCESS
	if err := repo.FinishPurchase(ctx, key1, "SUCCESS", "TRX-OK", "success"); err != nil {
		t.Fatalf("failed to finish purchase: %v", err)
	}

	// 5. Immediate re-purchase of the SAME package (opt1) within 30s cooldown must be rejected
	key3 := "tx-token-3"
	reserved, err = repo.ReservePurchase(ctx, key3, msisdn, opt1, "balance")
	if err != nil || reserved {
		t.Fatalf("expected re-purchase of same package within 30s cooldown to be rejected")
	}

	// 6. Purchase of a DIFFERENT package (opt2) after first is finished must be allowed
	key4 := "tx-token-4"
	reserved, err = repo.ReservePurchase(ctx, key4, msisdn, opt2, "balance")
	if err != nil || !reserved {
		t.Fatalf("expected purchase of different package to be allowed: %v", err)
	}

	// 7. Finish opt2 as FAILED
	if err := repo.FinishPurchase(ctx, key4, "FAILED", "", "insufficient balance"); err != nil {
		t.Fatalf("failed to finish purchase: %v", err)
	}

	// 8. Purchase of opt2 again after failure must be allowed immediately (no cooldown penalty on failure)
	key5 := "tx-token-5"
	reserved, err = repo.ReservePurchase(ctx, key5, msisdn, opt2, "balance")
	if err != nil || !reserved {
		t.Fatalf("expected retry after failure to be allowed: %v", err)
	}
}

func TestPendingQRIS_RepositoryStorageExpiryAndPruning(t *testing.T) {
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
	msisdn := "6281234567890"
	now := time.Now().UTC()

	// 1. Save valid pending QRIS (expires in 5 minutes)
	item1 := &PendingQRIS{
		TransactionCode: "TX-VALID-1",
		IdempotencyKey:  "IDEMP-1",
		MSISDN:          msisdn,
		OptionCode:      "OPT-1",
		PackageName:     "Paket A 10GB",
		Price:           25000,
		QRCode:          "00020101021226570011ID.CO.QRIS.WWW...",
		Status:          "PENDING",
		CreatedAt:       now,
		ExpiresAt:       now.Add(5 * time.Minute),
	}
	if err := repo.SavePendingQRIS(ctx, item1); err != nil {
		t.Fatalf("SavePendingQRIS failed: %v", err)
	}

	// 2. Query pending QRIS -> should return item1
	got, err := repo.GetPendingQRIS(ctx, msisdn)
	if err != nil || got == nil {
		t.Fatalf("GetPendingQRIS failed: %v, got: %v", err, got)
	}
	if got.TransactionCode != "TX-VALID-1" || got.PackageName != "Paket A 10GB" {
		t.Errorf("unexpected got: %+v", got)
	}

	// 3. Delete pending QRIS
	if err := repo.DeletePendingQRIS(ctx, "TX-VALID-1"); err != nil {
		t.Fatalf("DeletePendingQRIS failed: %v", err)
	}
	got, err = repo.GetPendingQRIS(ctx, msisdn)
	if err != nil || got != nil {
		t.Fatalf("expected nil after delete, got: %v", got)
	}

	// 4. Save expired pending QRIS (expired 1 minute ago)
	itemExpired := &PendingQRIS{
		TransactionCode: "TX-EXPIRED-1",
		IdempotencyKey:  "IDEMP-EXP-1",
		MSISDN:          msisdn,
		OptionCode:      "OPT-EXP",
		PackageName:     "Paket Expired",
		Price:           15000,
		QRCode:          "00020101...",
		Status:          "PENDING",
		CreatedAt:       now.Add(-6 * time.Minute),
		ExpiresAt:       now.Add(-1 * time.Minute),
	}
	if err := repo.SavePendingQRIS(ctx, itemExpired); err != nil {
		t.Fatalf("SavePendingQRIS failed: %v", err)
	}

	// 5. Query pending QRIS -> Auto-pruning should remove it and return nil
	got, err = repo.GetPendingQRIS(ctx, msisdn)
	if err != nil || got != nil {
		t.Fatalf("expected nil for expired QRIS due to auto-prune, got: %v, err: %v", got, err)
	}

	// Verify database row was physically pruned
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM myxl_pending_qris WHERE transaction_code = ?", "TX-EXPIRED-1").Scan(&count)
	if err != nil || count != 0 {
		t.Errorf("expected expired row to be deleted from table, count: %d, err: %v", count, err)
	}

	// 6. Test PruneExpiredQRIS directly
	_ = repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-EXP-A",
		IdempotencyKey:  "IDEMP-A",
		MSISDN:          msisdn,
		OptionCode:      "OPT-A",
		PackageName:     "Paket A",
		Price:           10000,
		QRCode:          "00020101...",
		Status:          "PENDING",
		CreatedAt:       now.Add(-10 * time.Minute),
		ExpiresAt:       now.Add(-5 * time.Minute),
	})
	_ = repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-EXP-B",
		IdempotencyKey:  "IDEMP-B",
		MSISDN:          msisdn,
		OptionCode:      "OPT-B",
		PackageName:     "Paket B",
		Price:           20000,
		QRCode:          "00020101...",
		Status:          "PENDING",
		CreatedAt:       now.Add(-8 * time.Minute),
		ExpiresAt:       now.Add(-3 * time.Minute),
	})
	_ = repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-ACTIVE-C",
		IdempotencyKey:  "IDEMP-C",
		MSISDN:          msisdn,
		OptionCode:      "OPT-C",
		PackageName:     "Paket C",
		Price:           30000,
		QRCode:          "00020101...",
		Status:          "PENDING",
		CreatedAt:       now,
		ExpiresAt:       now.Add(5 * time.Minute),
	})

	pruned, err := repo.PruneExpiredQRIS(ctx)
	if err != nil {
		t.Fatalf("PruneExpiredQRIS failed: %v", err)
	}
	if pruned != 2 {
		t.Errorf("expected 2 pruned rows, got: %d", pruned)
	}

	active, err := repo.GetPendingQRIS(ctx, msisdn)
	if err != nil || active == nil || active.TransactionCode != "TX-ACTIVE-C" {
		t.Errorf("expected TX-ACTIVE-C to remain active, got: %v", active)
	}
}

func TestMyXLPlugin_PendingQRISCommand(t *testing.T) {
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
	plugin := New(repo, nil)

	svc := &mockTgService{}
	baseCtx := &core.Context{
		Ctx:     ctx,
		Sender:  &core.User{ID: 1001},
		Chat:    &core.Chat{ID: 1001},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerUser{UserID: 1001},
	}

	// 1. .myxl qris without active account
	ctxQRIS := *baseCtx
	ctxQRIS.Args = []string{"qris"}
	_ = plugin.handlePendingQRIS(&ctxQRIS, []string{})
	if !strings.Contains(svc.sent, "Tidak ada akun MyXL yang aktif") {
		t.Errorf("expected no active account message, got %s", svc.sent)
	}

	// Save active account
	now := time.Now().UTC()
	acc := &Account{
		MSISDN:         "6281234567890",
		IsActive:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
		TokenExpiresAt: now.Add(time.Hour),
	}
	_ = repo.Save(ctx, acc)

	// 2. .myxl qris with active account, but no pending QRIS
	_ = plugin.handlePendingQRIS(&ctxQRIS, []string{})
	if !strings.Contains(svc.sent, "Tidak ada transaksi QRIS aktif") {
		t.Errorf("expected no pending QRIS message, got %s", svc.sent)
	}

	// 3. Save active QRIS
	_ = repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-CMD-QRIS",
		IdempotencyKey:  "IDEMP-CMD",
		MSISDN:          acc.MSISDN,
		OptionCode:      "OPT-X",
		PackageName:     "Paket Kilat 5GB",
		Price:           15000,
		QRCode:          "00020101021226570011ID.CO.QRIS.WWW...",
		Status:          "PENDING",
		CreatedAt:       now,
		ExpiresAt:       now.Add(5 * time.Minute),
	})

	// .myxl qris -> should show active QRIS details
	_ = plugin.handlePendingQRIS(&ctxQRIS, []string{})
	if !strings.Contains(svc.sent, "TRANSAKSI QRIS AKTIF") || !strings.Contains(svc.sent, "Paket Kilat 5GB") {
		t.Errorf("expected active QRIS details, got %s", svc.sent)
	}

	// 4. .myxl qris cancel -> cancels transaction
	_ = plugin.handlePendingQRIS(&ctxQRIS, []string{"cancel"})
	if !strings.Contains(svc.sent, "berhasil dibatalkan") {
		t.Errorf("expected cancellation message, got %s", svc.sent)
	}

	// Verify it got deleted from repo
	p, err := repo.GetPendingQRIS(ctx, acc.MSISDN)
	if err != nil || p != nil {
		t.Errorf("expected pending QRIS to be cancelled, got: %v", p)
	}
}

func TestPluginRequiresCallbackStateForPurchaseMutations(t *testing.T) {
	p := &Plugin{}
	for _, action := range []string{"buy_confirm", "buy_cancel"} {
		if !p.RequiresCallbackState(action, "opaque") {
			t.Fatalf("expected %s to require callback state", action)
		}
	}
	for _, action := range []string{"refresh", "dashboard", "detail", "cancel_draft"} {
		if p.RequiresCallbackState(action, "opaque") {
			t.Fatalf("did not expect %s to require callback state", action)
		}
	}
}

func TestTruncateStringPreservesUTF8(t *testing.T) {
	got := truncateString("Modem 日本語 Rumah", 8)
	if got != "Modem 日本" {
		t.Fatalf("truncateString=%q, want %q", got, "Modem 日本")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateString produced invalid UTF-8: %q", got)
	}
}

func TestRepositorySetAliasEnforcesSharedAliasInvariant(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	if err := repo.Save(ctx, &Account{MSISDN: "6281912345678", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetAlias(ctx, "6281912345678", strings.Repeat("界", 25)); err == nil {
		t.Fatal("repository accepted alias beyond shared 24-rune limit")
	}
	if err := repo.SetAlias(ctx, "6281912345678", "bad\x00alias"); err == nil {
		t.Fatal("repository accepted control characters in alias")
	}
}

func TestPendingQRISRepositoryClampsExpiryToFiveMinutes(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	created := time.Now().UTC().Add(-time.Minute)
	if err := repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-CLAMP",
		MSISDN:          "6281912345678",
		QRCode:          "000201010212TEST",
		Status:          "PENDING",
		CreatedAt:       created,
		ExpiresAt:       created.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := repo.GetPendingQRIS(ctx, "6281912345678")
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil {
		t.Fatal("clamped pending QRIS unexpectedly missing")
	}
	want := created.Add(pendingQRISTTL)
	if delta := pending.ExpiresAt.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("ExpiresAt=%v, want about %v", pending.ExpiresAt, want)
	}
}

func TestSendQRPhotoUsesMediaResourceAndCleansTempFile(t *testing.T) {
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.ForOwner("myxl").TempDir()
	if err != nil {
		t.Fatal(err)
	}

	p := New(nil, nil)
	p.files = manager.ForOwner("myxl")
	client := &immediateMyXLTaskClient{}
	p.SetTaskClient(client)
	svc := &myXLQRService{}

	if err := p.sendQRPhoto(
		context.Background(),
		svc,
		&tg.InputPeerUser{UserID: 1},
		"000201010212TEST",
		"Paket Test",
		15000,
	); err != nil {
		t.Fatal(err)
	}
	if !svc.mediaSent {
		t.Fatal("QR media was not sent")
	}
	if !svc.heldMediaLease {
		t.Fatal("QR media send did not run under media resource lease")
	}
	if len(client.specs) != 1 {
		t.Fatalf("QR task count=%d, want 1", len(client.specs))
	}
	spec := client.specs[0]
	if spec.Pool != tasks.PoolID("general") || spec.ExecutionTimeout != myxlQRSendTimeout {
		t.Fatalf("QR task spec=%+v", spec)
	}
	if len(spec.Resources) != 1 || spec.Resources[0].Name != "media" || spec.Resources[0].Amount != 1 {
		t.Fatalf("QR resources=%+v, want media:1", spec.Resources)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("QR temp workspace leaked %d entries", len(entries))
	}
}

func TestMyXLManifestDeclaresTasks(t *testing.T) {
	manifest := Module.Manifest()
	for _, capability := range manifest.Capabilities {
		if capability == "tasks" {
			return
		}
	}
	t.Fatalf("MyXL manifest must declare tasks: %+v", manifest.Capabilities)
}

func TestMyXLManifestDeclaresFilesystemTemp(t *testing.T) {
	manifest := Module.Manifest()
	found := false
	for _, cap := range manifest.Capabilities {
		if cap == "filesystem.temp" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("MyXL manifest must declare filesystem.temp: %+v", manifest.Capabilities)
	}
}

func TestPendingQRISRepositoryRejectsOversizePayloadAndMissingDelete(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := database.RunFeatureMigrations(ctx, db, Module); err != nil {
		t.Fatal(err)
	}
	repo := NewSQLiteRepository(db)
	if err := repo.SavePendingQRIS(ctx, &PendingQRIS{
		TransactionCode: "TX-OVERSIZE",
		MSISDN:          "6281912345678",
		QRCode:          strings.Repeat("x", maxQRPayloadBytes+1),
		Status:          "PENDING",
	}); err == nil {
		t.Fatal("oversized QR payload should not be persisted")
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM myxl_pending_qris WHERE transaction_code = ?", "TX-OVERSIZE").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("oversized QR payload persisted %d rows", count)
	}
	if err := repo.DeletePendingQRIS(ctx, "TX-MISSING"); err == nil {
		t.Fatal("missing pending QRIS delete should report not found")
	}
}
