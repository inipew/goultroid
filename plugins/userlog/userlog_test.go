package userlog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
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
		Message: &core.Message{ID: 1},
	}
	if err := cmds[0].Handler(setLogCtx); err != nil {
		t.Fatalf("handleSetLog failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Log destination set") {
		t.Errorf("expected destination set notice, got %s", mockTG.edited)
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
		Message: &core.Message{ID: 2},
	}
	if err := cmds[1].Handler(statusCtx); err != nil {
		t.Fatalf("handleLogStatus failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "UserLog Configuration") {
		t.Errorf("expected config overview, got %s", mockTG.edited)
	}

	// 3. .log tags off
	toggleCtx := &core.Context{
		Ctx:     context.Background(),
		Svc:     mockTG,
		PeerID:  &tg.InputPeerChat{ChatID: 777},
		Message: &core.Message{ID: 3},
		Args:    []string{"tags", "off"},
	}
	if err := cmds[1].Handler(toggleCtx); err != nil {
		t.Fatalf("handleLogStatus toggle failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "DISABLED") {
		t.Errorf("expected DISABLED notice, got %s", mockTG.edited)
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
	if !strings.Contains(mockTG.sentText, "Tag / Mention Alert") {
		t.Errorf("expected mention alert logged, got %s", mockTG.sentText)
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
	if !strings.Contains(mockTG.sentText, "New Private Message") {
		t.Errorf("expected PM logged, got %s", mockTG.sentText)
	}
}
