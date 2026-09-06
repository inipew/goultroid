package scheduler

import (
	"context"
	"errors"
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
	core.MockTelegramServicer
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
func (m *mockService) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return nil
}
func (m *mockService) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return nil
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

func TestRegisterPeriodicTask_NotRunning(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	// Calling RegisterPeriodicTask BEFORE Start() should return an error and NOT panic
	err = engine.RegisterPeriodicTask("early_task", 1*time.Second, func(ctx context.Context) error {
		return nil
	})
	if err == nil {
		t.Fatalf("expected error when registering task on non-running engine, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReconstructInputPeer_Supergroup(t *testing.T) {
	peer := reconstructInputPeer("supergroup", 1234567, 987654)
	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("expected *tg.InputPeerChannel for 'supergroup', got %T", peer)
	}
	if ch.ChannelID != 1234567 || ch.AccessHash != 987654 {
		t.Errorf("unexpected channel peer fields: ID=%d, Hash=%d", ch.ChannelID, ch.AccessHash)
	}

	peerChan := reconstructInputPeer("channel", 55555, 11111)
	if ch2, ok := peerChan.(*tg.InputPeerChannel); !ok || ch2.ChannelID != 55555 {
		t.Fatalf("expected *tg.InputPeerChannel for 'channel'")
	}
}

func TestExecuteCommand_ThroughMiddleware(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")

	var panicRecovered int32
	_ = router.Register(core.Command{
		Name:       "paniccmd",
		Permission: core.PermissionOwner,
		Handler: func(c *core.Context) error {
			atomic.AddInt32(&panicRecovered, 1)
			panic("simulated command handler panic")
		},
	})

	perms := core.NewPermissions(1001, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	ctx := context.Background()
	_ = engine.Start(ctx)
	defer engine.Stop()

	// Scheduling a panicking command must NOT crash the bot; RecoveryMiddleware catches it
	_, err = engine.ScheduleOnce(ctx, 1234, "chat", 0, time.Now().Add(-10*time.Millisecond), ActionCommand, ".paniccmd", 1001)
	if err != nil {
		t.Fatalf("failed to schedule command: %v", err)
	}

	time.Sleep(700 * time.Millisecond)
	if atomic.LoadInt32(&panicRecovered) == 0 {
		t.Errorf("expected command handler to be invoked and recovered")
	}
}

func TestExecuteCommand_WithCreatorID(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	svc := &mockService{}
	router := core.NewRouter(".")

	var ownerCmdRan int32
	_ = router.Register(core.Command{
		Name:       "secretcmd",
		Permission: core.PermissionOwner,
		Handler: func(c *core.Context) error {
			atomic.AddInt32(&ownerCmdRan, 1)
			return nil
		},
	})

	ownerID := int64(1001)
	strangerID := int64(3003)
	perms := core.NewPermissions(ownerID, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return svc }, router, perms, zap.NewNop())

	ctx := context.Background()

	// 1. Non-owner tries to execute owner-only command via scheduler
	jobStranger := database.ScheduledJob{
		ID:         1,
		ChatID:     123,
		PeerType:   "chat",
		ActionType: ActionCommand,
		Payload:    ".secretcmd",
		CreatedBy:  strangerID,
	}
	engine.executeCommand(ctx, jobStranger)

	if atomic.LoadInt32(&ownerCmdRan) != 0 {
		t.Fatalf("expected owner-only command NOT to run when created by stranger")
	}

	// 2. Owner executes owner-only command via scheduler
	jobOwner := database.ScheduledJob{
		ID:         2,
		ChatID:     123,
		PeerType:   "chat",
		ActionType: ActionCommand,
		Payload:    ".secretcmd",
		CreatedBy:  ownerID,
	}
	_ = engine.executeCommand(ctx, jobOwner)

	if atomic.LoadInt32(&ownerCmdRan) != 1 {
		t.Fatalf("expected owner-only command to run when created by owner, ran %d times", atomic.LoadInt32(&ownerCmdRan))
	}

	// 3. Security Boundary: CreatedBy == 0 must NOT default to owner and must NOT run owner commands
	jobAnonymous := database.ScheduledJob{
		ID:         3,
		ChatID:     123,
		PeerType:   "chat",
		ActionType: ActionCommand,
		Payload:    ".secretcmd",
		CreatedBy:  0, // Unknown/unprivileged
	}
	_ = engine.executeCommand(ctx, jobAnonymous)

	if atomic.LoadInt32(&ownerCmdRan) != 1 {
		t.Fatalf("expected owner-only command NOT to run when CreatedBy == 0")
	}
}

func TestJob_RecordsFailure(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name: "failcmd",
		Handler: func(c *core.Context) error {
			return errors.New("command intentional failure")
		},
	})

	perms := core.NewPermissions(1001, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return &mockService{} }, router, perms, zap.NewNop())

	ctx := context.Background()

	// Create job in DB
	saved, err := engine.ScheduleRecurring(ctx, 123, "chat", 0, time.Minute, ActionCommand, ".failcmd", 1001)
	if err != nil {
		t.Fatalf("failed to schedule recurring: %v", err)
	}

	// Claim and execute job
	claimed, err := db.ClaimDueScheduledJobs(ctx, time.Now().Add(time.Hour), 1, 30*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("failed to claim job: %v", err)
	}

	_, cancel2 := context.WithCancel(ctx)
	engine.executeJob(ctx, claimed[0], cancel2)

	// Fetch from DB to verify failure was recorded and status is pending with backoff
	updated, err := db.GetScheduledJob(ctx, saved.ID)
	if err != nil {
		t.Fatalf("failed to get updated job: %v", err)
	}
	if updated.AttemptCount != 1 {
		t.Errorf("expected AttemptCount 1, got %d", updated.AttemptCount)
	}
	if updated.LastError != "command intentional failure" {
		t.Errorf("expected LastError 'command intentional failure', got %q", updated.LastError)
	}
	if updated.Status != database.JobStatusPending {
		t.Errorf("expected Status 'pending' after first failure, got %q", updated.Status)
	}
}

func TestEngine_ValidationAndInterval(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, []int64{})
	engine := NewEngine(db, func() core.TelegramServicer { return &mockService{} }, router, perms, zap.NewNop())

	ctx := context.Background()

	// 1. Recurring duration < 1s must be rejected
	_, err = engine.ScheduleRecurring(ctx, 123, "chat", 0, 500*time.Millisecond, ActionMessage, "hello")
	if err == nil {
		t.Errorf("expected error when scheduling recurring job with interval < 1s, got nil")
	}

	// 2. Invalid action type on ScheduleOnce must be rejected
	_, err = engine.ScheduleOnce(ctx, 123, "chat", 0, time.Now().Add(time.Minute), "unknown_action", "hello")
	if err == nil {
		t.Errorf("expected error when scheduling job with invalid action type, got nil")
	}

	// 3. Invalid action type on ScheduleRecurring must be rejected
	_, err = engine.ScheduleRecurring(ctx, 123, "chat", 0, 5*time.Second, "unknown_action", "hello")
	if err == nil {
		t.Errorf("expected error when scheduling recurring job with invalid action type, got nil")
	}
}

func TestEngine_DynamicPrincipalRevocation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	sudoID := int64(777)
	ownerID := int64(1001)
	perms := core.NewPermissions(ownerID, []int64{sudoID})

	var sudoCmdRan bool
	router.Register(core.Command{
		Name:       "sudocmd",
		Permission: core.PermissionSudo,
		Handler: func(ctx *core.Context) error {
			sudoCmdRan = true
			return nil
		},
	})

	engine := NewEngine(db, func() core.TelegramServicer { return &mockService{} }, router, perms, zap.NewNop())
	ctx := context.Background()

	// Schedule a command created by sudo user 777
	job, err := engine.ScheduleOnce(ctx, 12345, "chat", 0, time.Now().Add(-time.Minute), ActionCommand, ".sudocmd", sudoID)
	if err != nil {
		t.Fatalf("failed to schedule command: %v", err)
	}

	// REVOKE SUDO PRIVILEGES dynamically before job execution
	perms.RemoveSudo(sudoID)

	// Claim and execute job
	claimed, err := db.ClaimDueScheduledJobs(ctx, time.Now(), 1, 90*time.Second)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("failed to claim job: %v", err)
	}

	_, cancel2 := context.WithCancel(ctx)
	engine.executeJob(ctx, claimed[0], cancel2)

	if sudoCmdRan {
		t.Fatalf("command should NOT have run after sudo revocation!")
	}

	// Verify job failed closed with permission denied
	updated, err := db.GetScheduledJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if updated.LastError != core.ErrPermissionDenied.Error() {
		t.Errorf("expected LastError %q, got %q", core.ErrPermissionDenied.Error(), updated.LastError)
	}
}

func TestEngine_StopWithTimeout_Bounded(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, nil)
	engine := NewEngine(db, func() core.TelegramServicer { return &mockService{} }, router, perms, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}

	// Simulate an uncooperative background task holding wg
	engine.wg.Add(1)
	defer engine.wg.Done() // ensure test cleanup doesn't deadlock

	// Stop with short timeout (50ms) -> MUST return error within timeout
	t0 := time.Now()
	err = engine.StopWithTimeout(50 * time.Millisecond)
	elapsed := time.Since(t0)

	if err == nil {
		t.Errorf("expected timeout error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("StopWithTimeout hung longer than bounded timeout: took %v", elapsed)
	}
}
