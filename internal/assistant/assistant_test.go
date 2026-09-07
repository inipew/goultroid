package assistant_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/services/inline"
	"go.uber.org/zap"
)

func TestBridge(t *testing.T) {
	b := assistant.NewBridge()
	ctx := context.Background()

	var receivedCount int32
	unsub := b.Subscribe(func(ctx context.Context, e assistant.Event) error {
		if e.Title == "Test Event" {
			atomic.AddInt32(&receivedCount, 1)
		}
		return nil
	})

	b.Dispatch(ctx, assistant.Event{
		Type:    assistant.EventNotification,
		Title:   "Test Event",
		Message: "Hello from bridge",
	})

	// Wait briefly for asynchronous dispatch
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&receivedCount) != 1 {
		t.Errorf("expected 1 event received, got %d", receivedCount)
	}

	// Test unsubscribe
	unsub()
	b.Dispatch(ctx, assistant.Event{
		Type:    assistant.EventNotification,
		Title:   "Test Event",
		Message: "Should not be received",
	})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&receivedCount) != 1 {
		t.Errorf("expected receivedCount to remain 1 after unsubscribe, got %d", receivedCount)
	}
}

func TestBotClient_Initialization(t *testing.T) {
	client := assistant.NewBotClient(1234, "hash", "", zap.NewNop())

	if client.IsRunning() {
		t.Errorf("expected client to not be running initially")
	}
	if client.Username() != "" {
		t.Errorf("expected empty username initially, got %s", client.Username())
	}

	// Should fail to start without bot token
	ctx := context.Background()
	err := client.Start(ctx)
	if !errors.Is(err, assistant.ErrBotTokenRequired) {
		t.Errorf("expected ErrBotTokenRequired, got %v", err)
	}

	// Test setters
	cbStore := callback.NewStateStore()
	cbRouter := callback.NewRouter(zap.NewNop(), cbStore)
	client.SetCallbackRouter(cbRouter)

	inlineEngine := inline.NewEngine(inline.NewRegistry(), zap.NewNop())
	client.SetInlineEngine(inlineEngine)

	bridge := assistant.NewBridge()
	client.SetBridge(bridge)
	if client.Bridge() != bridge {
		t.Errorf("expected set bridge to match")
	}

	// Test Stop on unstarted client
	if err := client.Stop(ctx); err != nil {
		t.Errorf("Stop failed: %v", err)
	}
}

func TestAssistantMenu_Render(t *testing.T) {
	startMenu := assistant.RenderStartMenu("TestBot", time.Now().Add(-10*time.Minute))
	if startMenu == nil {
		t.Fatal("expected non-nil start menu screen")
	}
	markup := startMenu.Markup()
	if len(markup.Rows) != 3 {
		t.Fatalf("expected 3 rows of buttons in start menu, got %d", len(markup.Rows))
	}

	// Verify all button callback data is valid v1 format
	for rowIdx, row := range markup.Rows {
		for btnIdx, btn := range row {
			if len(btn.Data) > 0 {
				ns, act, oid, err := callback.ParseCallbackData(btn.Data)
				if err != nil {
					t.Fatalf("invalid callback data on row %d btn %d (%s): %v", rowIdx, btnIdx, btn.Text, err)
				}
				if ns == "" || act == "" || oid == "" {
					t.Fatalf("empty fields in callback data: ns=%q act=%q oid=%q", ns, act, oid)
				}
			}
		}
	}

	statusMenu := assistant.RenderStatusScreen("TestBot", time.Now().Add(-10*time.Minute))
	if statusMenu == nil {
		t.Fatal("expected non-nil status menu screen")
	}
	if len(statusMenu.Markup().Rows) != 1 {
		t.Fatalf("expected 1 row of buttons in status menu, got %d", len(statusMenu.Markup().Rows))
	}
}

type mockTelegramServicer struct {
	core.TelegramServicer
	lastAnswer      string
	lastAlert       bool
	lastEditedText  string
	lastDeletedPeer tg.InputPeerClass
	lastDeletedIDs  []int
}

func (m *mockTelegramServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	m.lastAnswer = text
	m.lastAlert = alert
	return nil
}

func (m *mockTelegramServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.lastEditedText = text
	return nil
}

func (m *mockTelegramServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	m.lastDeletedPeer = peer
	m.lastDeletedIDs = msgIDs
	return nil
}

func TestAssistantHandler(t *testing.T) {
	mockSvc := &mockTelegramServicer{}
	h := assistant.NewHandler(nil, time.Now().Add(-5*time.Minute))

	if h.Namespace() != "assistant" {
		t.Errorf("expected namespace 'assistant', got %q", h.Namespace())
	}
	if !h.CallbackOptions().AutoAnswer {
		t.Errorf("expected AutoAnswer to be true")
	}

	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	// 1. Test status action
	cbCtx := &callback.CallbackContext{
		Ctx:       ctx,
		QueryID:   101,
		UserID:    12345,
		Namespace: "assistant",
		Action:    "status",
		OpaqueID:  "noop",
		Service:   mockSvc,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: 55,
		},
	}
	if err := h.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback status failed: %v", err)
	}
	if mockSvc.lastEditedText == "" {
		t.Error("expected edited text on status action")
	}

	// 2. Test ping action
	cbCtx.Action = "ping"
	if err := h.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback ping failed: %v", err)
	}
	if mockSvc.lastAnswer != "🏓 Pong!" || !mockSvc.lastAlert {
		t.Errorf("expected ping alert answer, got text=%q alert=%v", mockSvc.lastAnswer, mockSvc.lastAlert)
	}

	// 3. Test start action
	cbCtx.Action = "start"
	if err := h.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback start failed: %v", err)
	}

	// 4. Test close action
	cbCtx.Action = "close"
	if err := h.HandleCallback(cbCtx); err != nil {
		t.Fatalf("HandleCallback close failed: %v", err)
	}
	if len(mockSvc.lastDeletedIDs) != 1 || mockSvc.lastDeletedIDs[0] != 55 {
		t.Errorf("expected message 55 deleted on close, got %v", mockSvc.lastDeletedIDs)
	}
}

func TestBotServiceAdapter_Unsupported(t *testing.T) {
	adapter := assistant.NewBotServiceAdapter(nil, zap.NewNop())

	if !adapter.IsBotSent(100) {
		t.Error("expected IsBotSent to be true")
	}

	ctx := context.Background()
	if err := adapter.BanUser(ctx, nil, nil, 0); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on BanUser, got %v", err)
	}
	if err := adapter.UnbanUser(ctx, nil, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on UnbanUser, got %v", err)
	}
	if err := adapter.KickUser(ctx, nil, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on KickUser, got %v", err)
	}
	if _, err := adapter.GetFullChat(ctx, nil); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on GetFullChat, got %v", err)
	}
	if _, err := adapter.GetContacts(ctx); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on GetContacts, got %v", err)
	}
	if _, err := adapter.GetDialogs(ctx, 10); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported on GetDialogs, got %v", err)
	}
}

func TestBotClient_EntityAccessHashCache(t *testing.T) {
	client := assistant.NewBotClient(1234, "hash", "token", zap.NewNop())

	// Test initial empty state
	if hash := client.GetUserAccessHash(999); hash != 0 {
		t.Errorf("expected 0 for unrecorded user, got %d", hash)
	}
	if hash := client.GetChannelAccessHash(888); hash != 0 {
		t.Errorf("expected 0 for unrecorded channel, got %d", hash)
	}

	// Test SetUserAccessHash and SetChannelAccessHash
	client.SetUserAccessHash(999, 123456789)
	client.SetChannelAccessHash(888, 987654321)

	if hash := client.GetUserAccessHash(999); hash != 123456789 {
		t.Errorf("expected 123456789, got %d", hash)
	}
	if hash := client.GetChannelAccessHash(888); hash != 987654321 {
		t.Errorf("expected 987654321, got %d", hash)
	}

	// Test CacheEntities bulk cache
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			1001: {ID: 1001, AccessHash: 55555},
		},
		Channels: map[int64]*tg.Channel{
			2002: {ID: 2002, AccessHash: 77777},
		},
	}
	client.CacheEntities(entities)

	if hash := client.GetUserAccessHash(1001); hash != 55555 {
		t.Errorf("expected 55555 for user 1001, got %d", hash)
	}
	if hash := client.GetChannelAccessHash(2002); hash != 77777 {
		t.Errorf("expected 77777 for channel 2002, got %d", hash)
	}
}


