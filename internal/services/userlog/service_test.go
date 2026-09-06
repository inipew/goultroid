package userlog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
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

func TestUserLogService(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlog.NewService(db, mockTG, zap.NewNop())

	ctx := context.Background()

	// 1. Initial state without log chat: should not send anything
	err := svc.LogMention(ctx, "Test Group", "Alice", 111, "Hello @user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockTG.sentText != "" {
		t.Errorf("expected no message sent when log chat is unset, got %s", mockTG.sentText)
	}

	// 2. Set log chat
	if err := svc.SetLogChat(ctx, 999); err != nil {
		t.Fatalf("SetLogChat failed: %v", err)
	}
	chat, err := svc.GetLogChat(ctx)
	if err != nil || chat != 999 {
		t.Errorf("expected chat ID 999, got %d (err=%v)", chat, err)
	}

	// 3. Log mention
	if err := svc.LogMention(ctx, "Cool Group", "Bob", 222, "Hey check this out!"); err != nil {
		t.Fatalf("LogMention failed: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "Tag / Mention Alert") || !strings.Contains(mockTG.sentText, "Cool Group") {
		t.Errorf("expected mention alert in log chat, got %s", mockTG.sentText)
	}

	// 4. Log PM
	if err := svc.LogPM(ctx, "Charlie", 333, "Private message content"); err != nil {
		t.Fatalf("LogPM failed: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "New Private Message") {
		t.Errorf("expected PM notification in log chat, got %s", mockTG.sentText)
	}

	// 5. Log Action
	if err := svc.LogAction(ctx, "ban", 444, "Spamming links"); err != nil {
		t.Fatalf("LogAction failed: %v", err)
	}
	if !strings.Contains(mockTG.sentText, "Admin Action Executed") {
		t.Errorf("expected action log in log chat, got %s", mockTG.sentText)
	}
}
