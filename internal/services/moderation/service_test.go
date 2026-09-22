package moderation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type recordingModService struct {
	core.MockTelegramServicer
	mu        sync.Mutex
	muted     bool
	kicked    bool
	banned    bool
	muteCalls int
	muteErr   error
}

func (r *recordingModService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	r.mu.Lock()
	r.muted = true
	r.muteCalls++
	err := r.muteErr
	r.mu.Unlock()
	return err
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

func TestP7IWarnWithServiceUsesCallerTransport(t *testing.T) {
	defaultSvc := &recordingModService{}
	assistantSvc := &recordingModService{}
	service := newTestService(t, defaultSvc)

	res, err := service.WarnWithService(
		context.Background(),
		assistantSvc,
		&tg.InputPeerChat{ChatID: 100},
		&tg.InputPeerUser{UserID: 200},
		100,
		200,
		"Assistant warning",
		999,
		1,
		ActionMute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.ActionTaken != ActionMute || !assistantSvc.muted {
		t.Fatalf("Assistant transport result=%+v muted=%v", res, assistantSvc.muted)
	}
	if defaultSvc.muted || defaultSvc.kicked || defaultSvc.banned {
		t.Fatalf("P7-I warning escaped through default userbot transport: %+v", defaultSvc)
	}
}

func TestP7IFailedThresholdRetryDoesNotGrowWarnings(t *testing.T) {
	mockSvc := &recordingModService{muteErr: errors.New("telegram unavailable")}
	service := newTestService(t, mockSvc)
	ctx := context.Background()
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	for attempt := 0; attempt < 6; attempt++ {
		_, _ = service.Warn(ctx, peer, user, 100, 200, "retry", 999, 3, ActionMute)
	}
	count, err := service.GetWarningCount(ctx, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("failed enforcement warning count=%d, want bounded threshold 3", count)
	}
}

func TestP7IWarningBoundsRejectBeforePersistence(t *testing.T) {
	repo := &memoryWarningRepository{}
	service := NewService(repo, &recordingModService{}, zap.NewNop())
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	_, err := service.Warn(
		context.Background(),
		peer,
		user,
		100,
		200,
		strings.Repeat("x", MaxWarningReasonBytes+1),
		999,
		DefaultWarnThreshold,
		ActionMute,
	)
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("oversized reason error=%v, want ErrInvalidArgs", err)
	}
	if len(repo.records) != 0 {
		t.Fatalf("oversized reason persisted %d rows", len(repo.records))
	}

	_, err = service.Warn(
		context.Background(),
		peer,
		user,
		100,
		200,
		"bounded",
		999,
		MaxWarningThreshold+1,
		ActionMute,
	)
	if !errors.Is(err, core.ErrResourceLimit) {
		t.Fatalf("oversized threshold error=%v, want ErrResourceLimit", err)
	}
	if len(repo.records) != 0 {
		t.Fatalf("oversized threshold persisted %d rows", len(repo.records))
	}
}

func TestP7IConcurrentSameTargetWarnHasSingleThresholdEnforcement(t *testing.T) {
	repo := &memoryWarningRepository{}
	transport := &recordingModService{}
	service := NewService(repo, transport, zap.NewNop())
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	start := make(chan struct{})
	errs := make(chan error, DefaultWarnThreshold)
	var wg sync.WaitGroup
	for i := 0; i < DefaultWarnThreshold; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Warn(
				context.Background(),
				peer,
				user,
				100,
				200,
				"concurrent",
				999,
				DefaultWarnThreshold,
				ActionMute,
			)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent warn returned error: %v", err)
		}
	}
	transport.mu.Lock()
	muteCalls := transport.muteCalls
	transport.mu.Unlock()
	if muteCalls != 1 {
		t.Fatalf("threshold enforcement calls=%d, want 1", muteCalls)
	}
	count, err := service.GetWarningCount(context.Background(), 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("warnings after successful threshold enforcement=%d, want 0", count)
	}
}

type p7iBlockingMuteService struct {
	core.MockTelegramServicer
	entered chan struct{}
	release chan struct{}
}

func (s *p7iBlockingMuteService) MuteUser(
	context.Context,
	tg.InputPeerClass,
	tg.InputPeerClass,
	int,
) error {
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.release
	return nil
}

func TestP7IResetWaitsForSameTargetWarningSequence(t *testing.T) {
	repo := &memoryWarningRepository{}
	transport := &p7iBlockingMuteService{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := NewService(repo, transport, zap.NewNop())
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	warnDone := make(chan error, 1)
	go func() {
		_, err := service.Warn(
			context.Background(),
			peer,
			user,
			100,
			200,
			"threshold",
			999,
			1,
			ActionMute,
		)
		warnDone <- err
	}()

	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("warning did not reach threshold enforcement")
	}

	resetDone := make(chan error, 1)
	go func() {
		resetDone <- service.ResetWarnings(context.Background(), 100, 200)
	}()

	select {
	case err := <-resetDone:
		t.Fatalf("reset crossed same-target warning stripe before enforcement completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(transport.release)
	if err := <-warnDone; err != nil {
		t.Fatal(err)
	}
	if err := <-resetDone; err != nil {
		t.Fatal(err)
	}
	count, err := service.GetWarningCount(context.Background(), 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("warnings after serialized enforcement/reset=%d, want 0", count)
	}
}

func TestP7IWarningCoordinatesRejectBeforePersistence(t *testing.T) {
	repo := &memoryWarningRepository{}
	service := NewService(repo, &recordingModService{}, zap.NewNop())
	peer := &tg.InputPeerChat{ChatID: 100}
	user := &tg.InputPeerUser{UserID: 200}

	for _, tc := range []struct {
		chatID int64
		userID int64
	}{
		{chatID: 0, userID: 200},
		{chatID: 100, userID: 0},
		{chatID: -1, userID: 200},
		{chatID: 100, userID: -1},
	} {
		_, err := service.Warn(
			context.Background(),
			peer,
			user,
			tc.chatID,
			tc.userID,
			"invalid coordinates",
			999,
			DefaultWarnThreshold,
			ActionMute,
		)
		if !errors.Is(err, core.ErrInvalidArgs) {
			t.Fatalf("Warn(chat=%d,user=%d) error=%v, want ErrInvalidArgs",
				tc.chatID, tc.userID, err)
		}
	}
	if len(repo.records) != 0 {
		t.Fatalf("invalid warning coordinates persisted %d rows", len(repo.records))
	}
}


func TestP7IGuardedWarningRejectsBeforePersistence(t *testing.T) {
	repo := &memoryWarningRepository{}
	service := NewService(repo, &recordingModService{}, zap.NewNop())
	guardCalls := 0

	_, err := service.WarnWithServiceGuarded(
		context.Background(),
		&recordingModService{},
		&tg.InputPeerChat{ChatID: 100},
		&tg.InputPeerUser{UserID: 200},
		100,
		200,
		"guarded",
		999,
		DefaultWarnThreshold,
		ActionMute,
		func(context.Context) error {
			guardCalls++
			return core.ErrGroupMutationTargetProtected
		},
	)
	if !errors.Is(err, core.ErrGroupMutationTargetProtected) {
		t.Fatalf("guarded warning error=%v, want ErrGroupMutationTargetProtected", err)
	}
	if guardCalls != 1 {
		t.Fatalf("guard calls=%d, want 1", guardCalls)
	}
	if len(repo.records) != 0 {
		t.Fatalf("guarded warning persisted %d rows", len(repo.records))
	}
}
