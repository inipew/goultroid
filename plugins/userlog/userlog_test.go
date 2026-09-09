package userlog_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	mu       sync.Mutex
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	m.sentText = text
	m.mu.Unlock()
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.mu.Lock()
	m.edited = text
	m.mu.Unlock()
	return nil
}

func (m *mockTelegram) getSent() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sentText
}

func (m *mockTelegram) getEdited() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.edited
}

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestUserLogPlugin(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(db, mockTG, zap.NewNop())
	p := userlog.New(svc, 12345)

	if p.Name() != "userlog" {
		t.Errorf("expected plugin name userlog, got %s", p.Name())
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}

	// 1. .setlog
	setLogCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}
	if err := cmds[0].Handler(setLogCtx); err != nil {
		t.Fatalf("handleSetLog failed: %v", err)
	}
	if !strings.Contains(mockTG.getEdited(), "Log destination verified & active!") {
		t.Errorf("expected destination verified notice, got %s", mockTG.getEdited())
	}

	chat, _ := svc.GetLogChat(context.Background())
	if chat != 777 {
		t.Errorf("expected log chat 777, got %d", chat)
	}

	// 2. .log status
	statusCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 2, IsOutgoing: true},
	}
	if err := cmds[1].Handler(statusCtx); err != nil {
		t.Fatalf("handleLogStatus failed: %v", err)
	}
	if !strings.Contains(mockTG.getEdited(), "UserLog Dashboard") {
		t.Errorf("expected dashboard overview, got %s", mockTG.getEdited())
	}

	// 3. .log tags off
	toggleCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 3, IsOutgoing: true},
		Args:    []string{"tags", "off"},
	}
	if err := cmds[1].Handler(toggleCtx); err != nil {
		t.Fatalf("handleLogStatus toggle failed: %v", err)
	}
	if !strings.Contains(mockTG.getEdited(), "DISABLED") {
		t.Errorf("expected DISABLED notice, got %s", mockTG.getEdited())
	}

	// 4. .log test
	testCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 4, IsOutgoing: true},
		Args:    []string{"test"},
	}
	if err := cmds[1].Handler(testCtx); err != nil {
		t.Fatalf("handleLogTest failed: %v", err)
	}
	if !strings.Contains(mockTG.getEdited(), "UserLog test successful!") {
		t.Errorf("expected test delivery result, got %s", mockTG.getEdited())
	}

	// 5. .log clear
	clearCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 5, IsOutgoing: true},
		Args:    []string{"clear"},
	}
	if err := cmds[1].Handler(clearCtx); err != nil {
		t.Fatalf("handleLogClear failed: %v", err)
	}
	if !strings.Contains(mockTG.getEdited(), "Log destination disabled") {
		t.Errorf("expected log cleared notice, got %s", mockTG.getEdited())
	}
}

func TestUserLogPlugin_HandleIncomingMessage(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(db, mockTG, zap.NewNop())
	_ = svc.SetLogChat(context.Background(), 777)
	p := userlog.New(svc, 12345)

	ctx := context.Background()
	e := tg.Entities{
		Users: map[int64]*tg.User{
			999: {ID: 999, FirstName: "Alice"},
		},
		Chats: map[int64]*tg.Chat{
			100: {ID: 100, Title: "General Chat"},
		},
	}

	// Incoming mention in group
	mentionMsg := &tg.Message{
		ID:      1,
		Out:     false,
		PeerID:  &tg.PeerChat{ChatID: 100},
		FromID:  &tg.PeerUser{UserID: 999},
		Message: "Hey @boss check this!",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMentionName{Offset: 4, Length: 5, UserID: 12345},
		},
	}

	if err := p.HandleIncomingMessage(ctx, e, mentionMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage failed: %v", err)
	}
	// UserLog queues work async; wait briefly for worker to process.
	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
		t.Errorf("expected mention alert logged, got %s", mockTG.getSent())
	}

	// Incoming PM
	pmMsg := &tg.Message{
		ID:      2,
		Out:     false,
		PeerID:  &tg.PeerUser{UserID: 999},
		FromID:  &tg.PeerUser{UserID: 999},
		Message: "Direct message for you",
	}
	if err := p.HandleIncomingMessage(ctx, e, pmMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage PM failed: %v", err)
	}
	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "New Private Message") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "New Private Message") {
		t.Errorf("expected PM logged, got %s", mockTG.getSent())
	}

	// Mention via msg.Mentioned flag (without entities)
	mockTG.mu.Lock()
	mockTG.sentText = ""
	mockTG.mu.Unlock()

	flagMentionMsg := &tg.Message{
		ID:        3,
		Out:       false,
		Mentioned: true,
		PeerID:    &tg.PeerChat{ChatID: 100},
		FromID:    &tg.PeerUser{UserID: 999},
		Message:   "Reply without entity mention",
	}
	if err := p.HandleIncomingMessage(ctx, e, flagMentionMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage flag mention failed: %v", err)
	}
	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
		t.Errorf("expected flag mention alert logged, got %s", mockTG.getSent())
	}

	// Mention via @username with p.SetOwnerUsername
	p.SetOwnerUsername("my_boss")
	mockTG.mu.Lock()
	mockTG.sentText = ""
	mockTG.mu.Unlock()

	usernameMentionMsg := &tg.Message{
		ID:      4,
		Out:     false,
		PeerID:  &tg.PeerChat{ChatID: 100},
		FromID:  &tg.PeerUser{UserID: 999},
		Message: "@my_boss please look at this!",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityMention{Offset: 0, Length: 8},
		},
	}
	if err := p.HandleIncomingMessage(ctx, e, usernameMentionMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage username mention failed: %v", err)
	}
	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "Tag / Mention Alert") {
		t.Errorf("expected username mention alert logged, got %s", mockTG.getSent())
	}

	// Bot message ignored
	mockTG.mu.Lock()
	mockTG.sentText = ""
	mockTG.mu.Unlock()

	e.Users[888] = &tg.User{ID: 888, FirstName: "SomeBot", Bot: true}
	botMsg := &tg.Message{
		ID:      5,
		Out:     false,
		PeerID:  &tg.PeerUser{UserID: 888},
		FromID:  &tg.PeerUser{UserID: 888},
		Message: "I am a bot message",
	}
	if err := p.HandleIncomingMessage(ctx, e, botMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage bot failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if mockTG.getSent() != "" {
		t.Errorf("expected bot message to be ignored, got: %s", mockTG.getSent())
	}

	// Message in destination log chat ignored (loop prevention)
	mockTG.mu.Lock()
	mockTG.sentText = ""
	mockTG.mu.Unlock()

	logChatMsg := &tg.Message{
		ID:        6,
		Out:       false,
		Mentioned: true,
		PeerID:    &tg.PeerChat{ChatID: 777}, // Same as log destination
		FromID:    &tg.PeerUser{UserID: 999},
		Message:   "Mention in log chat itself",
	}
	if err := p.HandleIncomingMessage(ctx, e, logChatMsg, false, ""); err != nil {
		t.Fatalf("HandleIncomingMessage in log chat failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if mockTG.getSent() != "" {
		t.Errorf("expected message in log chat to be ignored, got: %s", mockTG.getSent())
	}
}

func TestUserLogPlugin_AdminActionEvent(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(db, mockTG, zap.NewNop())
	_ = svc.SetLogChat(context.Background(), 777)
	p := userlog.New(svc, 12345)

	eventBus := core.NewEventBus()
	if err := eventBus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer eventBus.Close()
	p.SetEventBus(eventBus)

	eventBus.Publish(&core.AdminActionEvent{
		At:        time.Now(),
		Action:    "ban",
		ActorID:   12345,
		TargetID:  999,
		ChatID:    100,
		ChatTitle: "Dev Group",
		Reason:    "Spamming",
		Success:   true,
	})

	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "Admin Action Audit") && strings.Contains(mockTG.getSent(), "BAN") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "Admin Action Audit") || !strings.Contains(mockTG.getSent(), "BAN") {
		t.Errorf("expected admin action BAN logged, got %s", mockTG.getSent())
	}
}

func TestUserLogPlugin_PMPermitEvent(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlogSvc.NewService(db, mockTG, zap.NewNop())
	_ = svc.SetLogChat(context.Background(), 777)
	p := userlog.New(svc, 12345)

	eventBus := core.NewEventBus()
	if err := eventBus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	defer eventBus.Close()
	p.SetEventBus(eventBus)

	eventBus.Publish(&core.PMPermitEvent{
		At:      time.Now(),
		Action:  "block",
		UserID:  888,
		Reason:  "exceeded warn limit",
		Success: true,
	})

	for i := 0; i < 20; i++ {
		if strings.Contains(mockTG.getSent(), "PMPERMIT BLOCK") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(mockTG.getSent(), "PMPERMIT BLOCK") {
		t.Errorf("expected PMPERMIT BLOCK logged to userlog, got %s", mockTG.getSent())
	}
}

func TestUserLogPlugin_OwnedSubscriptionsClose(t *testing.T) {
	p := userlog.New(nil, 12345)
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	p.SetEventBus(bus)
	if got := bus.SubscriptionCount("plugin:userlog"); got != 2 {
		t.Fatalf("owned subscriptions = %d, want 2", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.ShutdownContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("plugin:userlog"); got != 0 {
		t.Fatalf("owned subscriptions after shutdown = %d, want 0", got)
	}
}
