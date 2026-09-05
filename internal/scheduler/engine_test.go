package scheduler

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"go.uber.org/zap"
)

type mockService struct {
	mu   sync.Mutex
	sent []string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.mu.Lock()
	m.sent = append(m.sent, text)
	m.mu.Unlock()
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return nil, nil
}
func (m *mockService) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}
func (m *mockService) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}
func (m *mockService) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}
func (m *mockService) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, nil
}
func (m *mockService) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}
func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (m *mockService) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (m *mockService) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}

func (m *mockService) LastSent() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return ""
	}
	return m.sent[len(m.sent)-1]
}

func (m *mockService) SentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		input    string
		expected time.Duration
		hasError bool
	}{
		{"10s", 10 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"15 mins", 15 * time.Minute, false},
		{"2 hours", 2 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"2d6h", 54 * time.Hour, false},
		{"", 0, true},
		{"invalid", 0, true},
	}

	for _, tc := range cases {
		dur, err := ParseDuration(tc.input)
		if tc.hasError {
			if err == nil {
				t.Errorf("expected error for %q, got nil", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.input, err)
			}
			if dur != tc.expected {
				t.Errorf("for %q, expected %v, got %v", tc.input, tc.expected, dur)
			}
		}
	}
}

func TestEngine_LifecycleAndTasks(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, []int64{1002})
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}

	// 1. Register periodic task
	var counter int32
	err = engine.RegisterPeriodicTask("heartbeat", 20*time.Millisecond, func(ctx context.Context) error {
		atomic.AddInt32(&counter, 1)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to register periodic task: %v", err)
	}

	time.Sleep(70 * time.Millisecond)
	val := atomic.LoadInt32(&counter)
	if val < 2 {
		t.Errorf("expected periodic task to run at least twice, ran %d times", val)
	}

	// 2. Unregister periodic task
	if err := engine.UnregisterPeriodicTask("heartbeat"); err != nil {
		t.Fatalf("failed to unregister task: %v", err)
	}
	snap := atomic.LoadInt32(&counter)
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&counter) != snap {
		t.Errorf("expected task to stop running after unregister")
	}

	// 3. Stop engine
	if err := engine.Stop(); err != nil {
		t.Fatalf("failed to stop engine: %v", err)
	}
}

func TestEngine_ScheduleOnceAndRecurring(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")
	var cmdRan int32
	_ = router.Register(core.Command{
		Name: "testcmd",
		Handler: func(c *core.Context) error {
			atomic.AddInt32(&cmdRan, 1)
			return c.Reply("command executed!")
		},
	})

	perms := core.NewPermissions(1001, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	chatID := int64(8888)

	// 1. Schedule a message once (due immediately)
	jobMsg, err := engine.ScheduleOnce(ctx, chatID, "chat", 0, time.Now().Add(-10*time.Millisecond), ActionMessage, "Hello from scheduler!")
	if err != nil {
		t.Fatalf("failed to schedule message: %v", err)
	}
	if jobMsg == nil || jobMsg.ID == 0 {
		t.Fatalf("expected created job with ID")
	}

	// Wait for engine tick to process
	time.Sleep(700 * time.Millisecond)

	if !strings.Contains(svc.LastSent(), "Hello from scheduler!") {
		t.Errorf("expected message to be sent, got: %s", svc.LastSent())
	}

	// Verify one-shot job is removed from DB
	list, err := engine.List(ctx, chatID)
	if err != nil {
		t.Fatalf("failed to list jobs: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 jobs left after one-shot completion, got %d", len(list))
	}

	// 2. Schedule a command (due immediately)
	_, err = engine.ScheduleOnce(ctx, chatID, "chat", 0, time.Now().Add(-10*time.Millisecond), ActionCommand, ".testcmd")
	if err != nil {
		t.Fatalf("failed to schedule command: %v", err)
	}

	time.Sleep(700 * time.Millisecond)
	if atomic.LoadInt32(&cmdRan) == 0 {
		t.Errorf("expected scheduled command to run")
	}

	// 3. Schedule recurring job & cancel it
	recJob, err := engine.ScheduleRecurring(ctx, chatID, "chat", 0, 1*time.Hour, ActionMessage, "Hourly ping")
	if err != nil {
		t.Fatalf("failed to schedule recurring job: %v", err)
	}

	activeList, err := engine.List(ctx, chatID)
	if err != nil || len(activeList) != 1 {
		t.Fatalf("expected 1 recurring job in list, got %d", len(activeList))
	}

	// Cancel job
	if err := engine.Cancel(ctx, recJob.ID); err != nil {
		t.Fatalf("failed to cancel job: %v", err)
	}
	afterCancel, _ := engine.List(ctx, chatID)
	if len(afterCancel) != 0 {
		t.Errorf("expected 0 jobs after cancel, got %d", len(afterCancel))
	}
}
