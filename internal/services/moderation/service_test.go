package moderation

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

type recordingModService struct {
	core.MockTelegramServicer
	muted  bool
	kicked bool
	banned bool
}

func (r *recordingModService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	r.muted = true
	return nil
}

func (r *recordingModService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	r.kicked = true
	return nil
}

func (r *recordingModService) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	r.banned = true
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

func TestModerationService_WarnWorkflow(t *testing.T) {
	db := setupTestDB(t)
	mockSvc := &recordingModService{}
	service := NewService(db, mockSvc, zap.NewNop())

	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}
	chatID := int64(100)
	userID := int64(200)

	// 1. Warn 1
	res1, err := service.Warn(ctx, peer, user, chatID, userID, "first warning", 999, 3, ActionMute)
	if err != nil {
		t.Fatalf("unexpected error on warn 1: %v", err)
	}
	if res1.CurrentCount != 1 || res1.ActionTaken != ActionNone {
		t.Errorf("unexpected res1: %+v", res1)
	}
	if mockSvc.muted {
		t.Errorf("should not be muted yet on warn 1")
	}

	// 2. Warn 2
	res2, err := service.Warn(ctx, peer, user, chatID, userID, "second warning", 999, 3, ActionMute)
	if err != nil {
		t.Fatalf("unexpected error on warn 2: %v", err)
	}
	if res2.CurrentCount != 2 || res2.ActionTaken != ActionNone {
		t.Errorf("unexpected res2: %+v", res2)
	}

	// 3. Warn 3 (Threshold reached -> triggers mute)
	res3, err := service.Warn(ctx, peer, user, chatID, userID, "third warning", 999, 3, ActionMute)
	if err != nil {
		t.Fatalf("unexpected error on warn 3: %v", err)
	}
	if res3.ActionTaken != ActionMute {
		t.Errorf("expected ActionMute, got %s", res3.ActionTaken)
	}
	if !mockSvc.muted {
		t.Errorf("expected MuteUser to be called on threshold")
	}

	// After action, count should be reset
	cnt, err := service.GetWarningCount(ctx, chatID, userID)
	if err != nil || cnt != 0 {
		t.Errorf("expected warnings to be reset to 0, got %d", cnt)
	}
}

func TestModerationService_KickOnThreshold(t *testing.T) {
	db := setupTestDB(t)
	mockSvc := &recordingModService{}
	service := NewService(db, mockSvc, zap.NewNop())

	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	// Warn with threshold 1 and ActionKick
	res, err := service.Warn(ctx, peer, user, 100, 200, "instant kick", 999, 1, ActionKick)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ActionTaken != ActionKick {
		t.Errorf("expected ActionKick, got %s", res.ActionTaken)
	}
	if !mockSvc.kicked {
		t.Errorf("expected KickUser to be called")
	}
}

func TestModerationService_ManualActions(t *testing.T) {
	db := setupTestDB(t)
	mockSvc := &recordingModService{}
	service := NewService(db, mockSvc, zap.NewNop())

	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	if err := service.Mute(ctx, peer, user, 10*time.Minute); err != nil {
		t.Errorf("Mute error: %v", err)
	}
	if err := service.Unmute(ctx, peer, user); err != nil {
		t.Errorf("Unmute error: %v", err)
	}
	if err := service.Ban(ctx, peer, user, 0); err != nil {
		t.Errorf("Ban error: %v", err)
	}
	if err := service.Unban(ctx, peer, user); err != nil {
		t.Errorf("Unban error: %v", err)
	}
	if err := service.Kick(ctx, peer, user); err != nil {
		t.Errorf("Kick error: %v", err)
	}
}
