package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/tasks"
)

type nativeMyXLTaskClient struct {
	mu        sync.Mutex
	submitted int
	last      tasks.WorkSpec
}

func (c *nativeMyXLTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.submitted++
	c.last = spec
	c.mu.Unlock()

	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			result.Outcome = tasks.OutcomeFailed
		}
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	return nil, nil
}

func (*nativeMyXLTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*nativeMyXLTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*nativeMyXLTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func (c *nativeMyXLTaskClient) snapshot() (int, tasks.WorkSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submitted, c.last
}

func TestP1E1NativeQuotaRefreshA2Lifecycle(t *testing.T) {
	const ownerID int64 = 1001
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
	if err := repo.Save(ctx, &Account{
		MSISDN:         "6281912345678",
		IsActive:       true,
		AccessToken:    "access-token",
		IDToken:        "id-token",
		RefreshToken:   "refresh-token",
		TokenExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v8/packages/balance-and-credit":
			payload := `{"status":"SUCCESS","message":"","data":{"balance":{"remaining":75000,"expired_at":1735689600}}}`
			xTime := time.Now().UnixMilli()
			xData, _ := EncryptXData(payload, xTime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
		case "/api/v8/packages/quota-details":
			payload := `{"status":"SUCCESS","message":"","data":{"quotas":[{"name":"Xtra Combo","expired_at":1735689600,"benefits":[{"name":"Utama","data_type":"DATA","remaining":5368709120,"total":10737418240}]}]}}`
			xTime := time.Now().UnixMilli()
			xData, _ := EncryptXData(payload, xTime, DefaultXDataKey)
			_ = json.NewEncoder(w).Encode(EncryptedBody{XData: xData, XTime: xTime})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultClientConfig()
	cfg.BaseAPIURL = server.URL
	netSvc := network.NewService(server.Client(), nil).ForOwner("myxl")
	p := New(repo, NewClient(cfg, repo, netSvc))

	catalog := feature.NewRegistry()
	scope1 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	registration1, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: scope1}, p.FeatureSpec())
	if err != nil {
		t.Fatalf("register feature generation 1: %v", err)
	}

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		registration1.Close()
		t.Fatal(err)
	}
	defer sessions.Close()
	actions := rootinteraction.NewDispatcher(sessions)
	taskClient := &nativeMyXLTaskClient{}
	tgSvc := &mockTgService{}
	adapter, err := nativeinteraction.New(
		catalog,
		sessions,
		actions,
		taskClient,
		func() core.TelegramServicer { return tgSvc },
		core.NewPermissions(ownerID, nil),
	)
	if err != nil {
		registration1.Close()
		t.Fatal(err)
	}
	cleanup1, err := p.BindNative(nativeinteraction.DriverRuntime{
		Interactions: adapter,
		Catalog:      catalog,
		Scope:        scope1,
	})
	if err != nil {
		registration1.Close()
		t.Fatal(err)
	}

	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 77}
	command := &core.Context{
		Ctx:     ctx,
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true},
		Svc:     tgSvc,
		PeerID:  peer,
	}
	if err := p.handleShowQuota(command, nil); err != nil {
		t.Fatalf("open native quota: %v", err)
	}
	if stats := sessions.Stats(); stats.Sessions != 1 {
		t.Fatalf("native quota sessions=%d, want 1", stats.Sessions)
	}

	firstData := nativeQuotaCallbackData(t, tgSvc.lastMarkup)
	if !rootinteraction.OwnsCallbackData(firstData) {
		t.Fatalf("native quota emitted non-a2 callback %q", firstData)
	}
	if _, err := rootinteraction.ParseCallbackToken(firstData); err != nil {
		t.Fatalf("parse native quota callback: %v", err)
	}

	wrongActor := nativeQuotaCallbackEvent(firstData, 11, ownerID+1, ownerID, 10, peer)
	if handled, err := adapter.HandleCallback(ctx, wrongActor); !handled || !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("wrong actor handled=%v err=%v, want binding mismatch", handled, err)
	}
	wrongTarget := nativeQuotaCallbackEvent(firstData, 12, ownerID, ownerID, 11, peer)
	if handled, err := adapter.HandleCallback(ctx, wrongTarget); !handled || !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("wrong target handled=%v err=%v, want binding mismatch", handled, err)
	}

	correct := nativeQuotaCallbackEvent(firstData, 13, ownerID, ownerID, 10, peer)
	if handled, err := adapter.HandleCallback(ctx, correct); !handled || err != nil {
		t.Fatalf("native quota refresh handled=%v err=%v", handled, err)
	}
	count, last := taskClient.snapshot()
	if count != 1 {
		t.Fatalf("TaskEngine submissions=%d, want 1", count)
	}
	if last.Scope != scope1 {
		t.Fatalf("TaskEngine scope=%+v, want %+v", last.Scope, scope1)
	}
	if last.ExecutionTimeout != nativeQuotaRefreshExec {
		t.Fatalf("TaskEngine execution timeout=%s, want %s", last.ExecutionTimeout, nativeQuotaRefreshExec)
	}

	secondData := nativeQuotaCallbackData(t, tgSvc.lastMarkup)
	if string(secondData) == string(firstData) {
		t.Fatal("quota refresh did not advance a2 revision")
	}
	replay := nativeQuotaCallbackEvent(firstData, 14, ownerID, ownerID, 10, peer)
	if handled, err := adapter.HandleCallback(ctx, replay); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale refresh handled=%v err=%v, want stale token", handled, err)
	}

	cleanup1()
	sessions.CancelScope(scope1)
	registration1.Close()
	oldAfterDisable := nativeQuotaCallbackEvent(secondData, 15, ownerID, ownerID, 10, peer)
	if handled, err := adapter.HandleCallback(ctx, oldAfterDisable); !handled || err == nil {
		t.Fatalf("disabled generation token handled=%v err=%v, want rejection", handled, err)
	}

	scope2 := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 2}
	registration2, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: scope2}, p.FeatureSpec())
	if err != nil {
		t.Fatalf("register feature generation 2: %v", err)
	}
	defer registration2.Close()
	cleanup2, err := p.BindNative(nativeinteraction.DriverRuntime{
		Interactions: adapter,
		Catalog:      catalog,
		Scope:        scope2,
	})
	if err != nil {
		t.Fatalf("bind feature generation 2: %v", err)
	}
	defer cleanup2()

	if err := p.handleShowQuota(command, nil); err != nil {
		t.Fatalf("open native quota after reload: %v", err)
	}
	reloadedData := nativeQuotaCallbackData(t, tgSvc.lastMarkup)
	if string(reloadedData) == string(secondData) {
		t.Fatal("reload reused old generation callback token")
	}
	if handled, err := adapter.HandleCallback(ctx, nativeQuotaCallbackEvent(reloadedData, 16, ownerID, ownerID, 10, peer)); !handled || err != nil {
		t.Fatalf("reloaded generation callback handled=%v err=%v", handled, err)
	}
}

func nativeQuotaCallbackData(t *testing.T, markup tg.ReplyMarkupClass) []byte {
	t.Helper()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || len(inline.Rows) != 1 || len(inline.Rows[0].Buttons) != 1 {
		t.Fatalf("native quota markup=%T %+v", markup, markup)
	}
	button, ok := inline.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("native quota button=%T", inline.Rows[0].Buttons[0])
	}
	return append([]byte(nil), button.Data...)
}

func nativeQuotaCallbackEvent(data []byte, queryID, userID, chatID int64, messageID int, peer tg.InputPeerClass) *core.CallbackQueryEvent {
	return &core.CallbackQueryEvent{
		QueryID: queryID,
		UserID:  userID,
		ChatID:  chatID,
		MsgID:   messageID,
		Data:    append([]byte(nil), data...),
		Origin:  core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: messageID,
		},
	}
}

func TestP1E3NativePurchaseConfirmCancelUseA2(t *testing.T) {
	const ownerID int64 = 1001
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
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:myxl", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()

	taskClient := &nativeMyXLTaskClient{}
	tgSvc := &mockTgService{}
	adapter, err := nativeinteraction.New(
		catalog,
		sessions,
		rootinteraction.NewDispatcher(sessions),
		taskClient,
		func() core.TelegramServicer { return tgSvc },
		core.NewPermissions(ownerID, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{
		Interactions: adapter,
		Catalog:      catalog,
		Scope:        scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	peer := &tg.InputPeerUser{UserID: ownerID, AccessHash: 77}
	newCommand := func() *core.Context {
		return &core.Context{
			Ctx:     ctx,
			Sender:  &core.User{ID: ownerID},
			Chat:    &core.Chat{ID: ownerID, Type: "private"},
			Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true},
			Svc:     tgSvc,
			PeerID:  peer,
			Args:    []string{"OPT-A", "balance"},
		}
	}

	if err := p.handleBuy(newCommand(), []string{"OPT-A", "balance"}); err != nil {
		t.Fatalf("open native purchase: %v", err)
	}
	if stats := sessions.Stats(); stats.Sessions != 1 {
		t.Fatalf("native purchase sessions=%d, want 1", stats.Sessions)
	}

	confirmData := nativePurchaseCallbackData(t, tgSvc.lastMarkup, "Konfirmasi")
	cancelData := nativePurchaseCallbackData(t, tgSvc.lastMarkup, "Batal")
	for label, data := range map[string][]byte{"confirm": confirmData, "cancel": cancelData} {
		if !rootinteraction.OwnsCallbackData(data) {
			t.Fatalf("%s callback is not a2: %q", label, data)
		}
		if _, err := rootinteraction.ParseCallbackToken(data); err != nil {
			t.Fatalf("parse %s callback: %v", label, err)
		}
	}

	if handled, err := adapter.HandleCallback(ctx, nativeQuotaCallbackEvent(cancelData, 21, ownerID, ownerID, 10, peer)); !handled || err != nil {
		t.Fatalf("native cancel handled=%v err=%v", handled, err)
	}
	if stats := sessions.Stats(); stats.Sessions != 0 {
		t.Fatalf("cancel left %d live sessions, want 0", stats.Sessions)
	}
	var reservations int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM myxl_purchase_requests").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("cancel created %d purchase reservations", reservations)
	}
	if handled, err := adapter.HandleCallback(ctx, nativeQuotaCallbackEvent(confirmData, 22, ownerID, ownerID, 10, peer)); !handled || err == nil {
		t.Fatalf("cancelled session old confirm handled=%v err=%v, want rejection", handled, err)
	}

	fixture.set(25000, "TOKEN-B", "Paket A")
	if err := p.handleBuy(newCommand(), []string{"OPT-A", "balance"}); err != nil {
		t.Fatalf("reopen native purchase: %v", err)
	}
	confirmData = nativePurchaseCallbackData(t, tgSvc.lastMarkup, "Konfirmasi")
	fixture.set(26000, "TOKEN-C", "Paket A")

	if handled, err := adapter.HandleCallback(ctx, nativeQuotaCallbackEvent(confirmData, 23, ownerID, ownerID, 10, peer)); !handled || err != nil {
		t.Fatalf("price-drift confirm handled=%v err=%v", handled, err)
	}
	count, last := taskClient.snapshot()
	if count != 2 {
		t.Fatalf("TaskEngine submissions=%d, want 2 (cancel + confirm)", count)
	}
	if last.Scope != scope {
		t.Fatalf("confirm TaskEngine scope=%+v, want %+v", last.Scope, scope)
	}
	if last.ExecutionTimeout != nativePurchaseConfirmExec {
		t.Fatalf("confirm execution timeout=%s, want %s", last.ExecutionTimeout, nativePurchaseConfirmExec)
	}
	if stats := sessions.Stats(); stats.Sessions != 0 {
		t.Fatalf("price-drift confirm left %d sessions, want 0", stats.Sessions)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM myxl_purchase_requests").Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("price-drift confirm created %d purchase reservations, want 0", reservations)
	}
}

func nativePurchaseCallbackData(t *testing.T, markup tg.ReplyMarkupClass, label string) []byte {
	t.Helper()
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok || inline == nil {
		t.Fatalf("native purchase markup=%T, want inline markup", markup)
	}
	for _, row := range inline.Rows {
		for _, button := range row.Buttons {
			callbackButton, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if strings.Contains(callbackButton.Text, label) {
				return append([]byte(nil), callbackButton.Data...)
			}
		}
	}
	t.Fatalf("native purchase callback %q not found", label)
	return nil
}
