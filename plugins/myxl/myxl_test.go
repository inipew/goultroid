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
	sent string
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
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *mockTgService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.sent = text
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

	// 5. Group chat security check: login should be blocked in group chats
	ctxGroup := *baseCtx
	ctxGroup.Chat = &core.Chat{ID: -100123456, Type: "supergroup"}
	ctxGroup.Args = []string{"login", "081912345678"}
	_ = cmdMap["myxl"].Handler(&ctxGroup)
	if !strings.Contains(svc.sent, "Private Message") {
		t.Errorf("expected group block security alert, got %s", svc.sent)
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

	// 8. .myxl accounts (should show 1 active account with alias)
	ctxAcc2 := *baseCtx
	ctxAcc2.Args = []string{"accounts"}
	_ = cmdMap["myxl"].Handler(&ctxAcc2)
	if !strings.Contains(svc.sent, "6281912345678") || !strings.Contains(svc.sent, "ModemHome") {
		t.Errorf("expected active account listed with alias, got %s", svc.sent)
	}

	// 9. .kuota (shortcut)
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

	// 10. Test Capabilities declaration
	caps := plugin.Capabilities()
	if len(caps) == 0 || caps[0].ID != "myxl" {
		t.Errorf("expected capability myxl, got %#v", caps)
	}

	// 11. Test Callback handler
	cbCtx := &callback.CallbackContext{
		Ctx:       ctx,
		Action:    "refresh",
		OpaqueID:  "6281912345678",
		Target:    core.CallbackTarget{Peer: &tg.InputPeerUser{UserID: 1001}, MessageID: 1},
		Namespace: "myxl",
		UserID:    1001,
		Service:   svc,
	}
	if err := plugin.HandleCallback(cbCtx); err != nil {
		t.Errorf("HandleCallback failed: %v", err)
	}

	// 12. .myxl del 081912345678
	ctxDel := *baseCtx
	ctxDel.Args = []string{"del", "081912345678"}
	_ = cmdMap["myxl"].Handler(&ctxDel)
	if !strings.Contains(svc.sent, "berhasil dihapus") {
		t.Errorf("expected deleted message, got %s", svc.sent)
	}
}
