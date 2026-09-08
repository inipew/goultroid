package moderation

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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

type memoryWarningRepository struct {
	records []*WarningRecord
	nextID  int
}

func (r *memoryWarningRepository) AddWarning(ctx context.Context, chatID, userID int64, reason string, warnedBy int64) error {
	r.nextID++
	r.records = append(r.records, &WarningRecord{ID: r.nextID, ChatID: chatID, UserID: userID, Reason: reason, WarnedBy: warnedBy, CreatedAt: time.Now().UTC()})
	return nil
}

func (r *memoryWarningRepository) GetWarnings(ctx context.Context, chatID, userID int64) ([]*WarningRecord, error) {
	result := make([]*WarningRecord, 0)
	for i := len(r.records) - 1; i >= 0; i-- {
		record := r.records[i]
		if record.ChatID == chatID && record.UserID == userID {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *memoryWarningRepository) GetWarningCount(ctx context.Context, chatID, userID int64) (int, error) {
	count := 0
	for _, record := range r.records {
		if record.ChatID == chatID && record.UserID == userID {
			count++
		}
	}
	return count, nil
}

func (r *memoryWarningRepository) ResetWarnings(ctx context.Context, chatID, userID int64) error {
	filtered := r.records[:0]
	for _, record := range r.records {
		if record.ChatID != chatID || record.UserID != userID {
			filtered = append(filtered, record)
		}
	}
	r.records = filtered
	return nil
}

func newTestService(t *testing.T, mockSvc core.TelegramServicer) *Service {
	t.Helper()
	return NewService(&memoryWarningRepository{}, mockSvc, zap.NewNop())
}

func TestModerationService_WarnWorkflow(t *testing.T) {
	mockSvc := &recordingModService{}
	service := newTestService(t, mockSvc)

	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}
	chatID := int64(100)
	userID := int64(200)

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

	res2, err := service.Warn(ctx, peer, user, chatID, userID, "second warning", 999, 3, ActionMute)
	if err != nil {
		t.Fatalf("unexpected error on warn 2: %v", err)
	}
	if res2.CurrentCount != 2 || res2.ActionTaken != ActionNone {
		t.Errorf("unexpected res2: %+v", res2)
	}

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

	cnt, err := service.GetWarningCount(ctx, chatID, userID)
	if err != nil || cnt != 0 {
		t.Errorf("expected warnings to be reset to 0, got %d", cnt)
	}
}

func TestModerationService_KickOnThreshold(t *testing.T) {
	mockSvc := &recordingModService{}
	service := newTestService(t, mockSvc)

	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

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
	mockSvc := &recordingModService{}
	service := newTestService(t, mockSvc)

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
