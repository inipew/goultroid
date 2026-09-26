package myxl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	"github.com/inipew/goultroid/internal/services/callback"
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
	legacy := callback.NewStateStore()
	p.SetStateStore(legacy)

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
	if got := legacy.Len(); got != 0 {
		t.Fatalf("native quota allocated %d legacy callback states, want 0", got)
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
	if got := legacy.Len(); got != 0 {
		t.Fatalf("native quota allocated legacy state after reload: %d", got)
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
