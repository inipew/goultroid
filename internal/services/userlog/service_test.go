package userlog_test

import (
	"context"
	"errors"
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
	lastPeer tg.InputPeerClass
	sendErr  error
	attempts int
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.attempts++
	m.lastPeer = peer
	m.sentText = text
	if m.sendErr != nil {
		return nil, m.sendErr
	}
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

func TestUserLogService_Basic(t *testing.T) {
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

	// 2. Set log chat (legacy method)
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
	if !strings.Contains(mockTG.sentText, "Admin Action Audit") {
		t.Errorf("expected action log in log chat, got %s", mockTG.sentText)
	}
}

func TestUserLogService_StructuredDestination(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlog.NewService(db, mockTG, zap.NewNop())
	ctx := context.Background()

	dest := userlog.LogDestination{
		Type:       userlog.LogDestinationChannel,
		ID:         1234567890,
		AccessHash: 9876543210,
		Title:      "My Audit Log Channel",
	}

	if err := svc.SetDestination(ctx, dest); err != nil {
		t.Fatalf("SetDestination failed: %v", err)
	}

	retrieved, err := svc.GetDestination(ctx)
	if err != nil {
		t.Fatalf("GetDestination failed: %v", err)
	}
	if retrieved == nil || retrieved.Type != userlog.LogDestinationChannel || retrieved.ID != 1234567890 || retrieved.AccessHash != 9876543210 {
		t.Fatalf("unexpected retrieved destination: %+v", retrieved)
	}

	// Verify delivery uses *tg.InputPeerChannel with correct AccessHash
	if err := svc.LogPM(ctx, "Eve", 555, "Important note"); err != nil {
		t.Fatalf("LogPM failed: %v", err)
	}

	chPeer, ok := mockTG.lastPeer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("expected *tg.InputPeerChannel, got %T", mockTG.lastPeer)
	}
	if chPeer.ChannelID != 1234567890 || chPeer.AccessHash != 9876543210 {
		t.Errorf("unexpected InputPeerChannel values: %+v", chPeer)
	}

	// IsLogDestination check
	if !svc.IsLogDestination(&tg.PeerChannel{ChannelID: 1234567890}) {
		t.Errorf("expected true for IsLogDestination with matching ChannelID")
	}
	if svc.IsLogDestination(&tg.PeerChannel{ChannelID: 999999999}) {
		t.Errorf("expected false for IsLogDestination with non-matching ChannelID")
	}
}

func TestUserLogService_LegacyFallback(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlog.NewService(db, mockTG, zap.NewNop())
	ctx := context.Background()

	// Simulate legacy database row: log_chat_id = -1001888888888
	if err := db.SetUserLogSetting(ctx, userlog.SettingLogChatID, "-1001888888888"); err != nil {
		t.Fatalf("failed to insert legacy setting: %v", err)
	}

	dest, err := svc.GetDestination(ctx)
	if err != nil {
		t.Fatalf("GetDestination failed on legacy data: %v", err)
	}
	if dest == nil {
		t.Fatalf("expected dest not nil")
	}
	if dest.Type != userlog.LogDestinationChannel || dest.ID != 1888888888 {
		t.Errorf("unexpected legacy normalized dest: %+v", dest)
	}
}

func TestUserLogService_UnicodeRuneTruncation(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlog.NewService(db, mockTG, zap.NewNop())
	ctx := context.Background()

	_ = svc.SetDestination(ctx, userlog.LogDestination{Type: userlog.LogDestinationChat, ID: 777})

	// 300 emoji characters (each 4 bytes)
	longEmojiText := strings.Repeat("🎉", 300)
	if err := svc.LogMention(ctx, "Emoji Chat", "User", 123, longEmojiText); err != nil {
		t.Fatalf("LogMention failed on long emoji string: %v", err)
	}

	if !strings.Contains(mockTG.sentText, "🎉...") {
		t.Errorf("expected clean rune truncation with ellipsis, got %s", mockTG.sentText)
	}
}

func TestUserLogService_DeliveryHealthAndStats(t *testing.T) {
	db := setupTestDB(t)
	mockTG := &mockTelegram{}
	svc := userlog.NewService(db, mockTG, zap.NewNop())
	ctx := context.Background()

	// 1. Initially unconfigured
	stats := svc.Stats(ctx)
	if stats.Status != userlog.StatusUnconfigured {
		t.Errorf("expected UNCONFIGURED, got %s", stats.Status)
	}

	_ = svc.SetDestination(ctx, userlog.LogDestination{Type: userlog.LogDestinationChat, ID: 888})

	// 2. Successful delivery
	duration, err := svc.SendTestMessage(ctx)
	if err != nil {
		t.Fatalf("SendTestMessage failed: %v", err)
	}
	if duration <= 0 {
		t.Errorf("expected non-zero latency measurement")
	}

	stats = svc.Stats(ctx)
	if stats.Status != userlog.StatusHealthy || stats.DeliveredCount != 1 {
		t.Errorf("unexpected healthy stats: %+v", stats)
	}

	// 3. Permanent failure: CHAT_WRITE_FORBIDDEN (fail-fast without retry)
	mockTG.attempts = 0
	mockTG.sendErr = errors.New("rpc error: code 400: CHAT_WRITE_FORBIDDEN")
	err = svc.LogPM(ctx, "Spammer", 999, "Hello")
	if err == nil {
		t.Fatalf("expected error from forbidden chat, got nil")
	}
	if mockTG.attempts != 1 {
		t.Errorf("expected fail-fast with 1 attempt for permanent error, got %d", mockTG.attempts)
	}

	stats = svc.Stats(ctx)
	if stats.Status != userlog.StatusDegraded || stats.FailedCount != 1 {
		t.Errorf("unexpected degraded stats: %+v", stats)
	}
}
