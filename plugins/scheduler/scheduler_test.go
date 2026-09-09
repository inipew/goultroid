package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/scheduler"
)

type mockSchedulerService struct {
	jobs     map[int64]*scheduler.ScheduledJob
	nextID   int64
	canceled []int64
}

func newMockSchedulerService() *mockSchedulerService {
	return &mockSchedulerService{
		jobs:   make(map[int64]*scheduler.ScheduledJob),
		nextID: 1,
	}
}

func (m *mockSchedulerService) RegisterPeriodicTask(name string, interval time.Duration, task scheduler.TaskFunc) error {
	return nil
}
func (m *mockSchedulerService) UnregisterPeriodicTask(name string) error {
	return nil
}
func (m *mockSchedulerService) ScheduleOnce(ctx context.Context, chatID int64, peerType string, accessHash int64, when time.Time, actionType string, payload string, creatorID ...int64) (*scheduler.ScheduledJob, error) {
	id := m.nextID
	m.nextID++
	var createdBy int64
	if len(creatorID) > 0 {
		createdBy = creatorID[0]
	}
	job := &scheduler.ScheduledJob{
		ID:         id,
		ChatID:     chatID,
		PeerType:   peerType,
		AccessHash: accessHash,
		ActionType: actionType,
		Payload:    payload,
		NextRunAt:  when,
		CreatedBy:  createdBy,
	}
	m.jobs[id] = job
	return job, nil
}
func (m *mockSchedulerService) ScheduleRecurring(ctx context.Context, chatID int64, peerType string, accessHash int64, interval time.Duration, actionType string, payload string, creatorID ...int64) (*scheduler.ScheduledJob, error) {
	id := m.nextID
	m.nextID++
	var createdBy int64
	if len(creatorID) > 0 {
		createdBy = creatorID[0]
	}
	job := &scheduler.ScheduledJob{
		ID:              id,
		ChatID:          chatID,
		PeerType:        peerType,
		AccessHash:      accessHash,
		ActionType:      actionType,
		Payload:         payload,
		IntervalSeconds: int64(interval.Seconds()),
		NextRunAt:       time.Now().Add(interval),
		CreatedBy:       createdBy,
	}
	m.jobs[id] = job
	return job, nil
}
func (m *mockSchedulerService) Cancel(ctx context.Context, jobID int64) error {
	delete(m.jobs, jobID)
	m.canceled = append(m.canceled, jobID)
	return nil
}
func (m *mockSchedulerService) CancelScoped(ctx context.Context, requesterID, chatID, jobID int64) error {
	job, ok := m.jobs[jobID]
	if !ok {
		return core.ErrNotFound
	}
	if requesterID <= 0 || chatID == 0 || job.ChatID != chatID || job.CreatedBy != requesterID {
		return core.ErrPermissionDenied
	}
	return m.Cancel(ctx, jobID)
}
func (m *mockSchedulerService) List(ctx context.Context, chatID int64) ([]scheduler.ScheduledJob, error) {
	var list []scheduler.ScheduledJob
	for _, j := range m.jobs {
		if j.ChatID == chatID {
			list = append(list, *j)
		}
	}
	return list, nil
}
func (m *mockSchedulerService) JobHistory(ctx context.Context, jobID int64, limit int) ([]scheduler.JobHistoryEntry, error) {
	return nil, nil
}
func (m *mockSchedulerService) JobHistoryScoped(ctx context.Context, requesterID, chatID, jobID int64, limit int) ([]scheduler.JobHistoryEntry, error) {
	job, ok := m.jobs[jobID]
	if !ok {
		return nil, core.ErrNotFound
	}
	if requesterID <= 0 || chatID == 0 || job.ChatID != chatID || job.CreatedBy != requesterID {
		return nil, core.ErrPermissionDenied
	}
	return m.JobHistory(ctx, jobID, limit)
}
func (m *mockSchedulerService) Start(ctx context.Context) error { return nil }
func (m *mockSchedulerService) Stop(ctx context.Context) error  { return nil }

type mockTelegramServicer struct {
	core.MockTelegramServicer
	sent string
}

func (m *mockTelegramServicer) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockTelegramServicer) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (m *mockTelegramServicer) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockTelegramServicer) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockTelegramServicer) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if msgID == 99 {
		return &tg.Message{ID: 99, Message: "Take a break"}, nil
	}
	return nil, nil
}
func (m *mockTelegramServicer) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}
func (m *mockTelegramServicer) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}
func (m *mockTelegramServicer) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockTelegramServicer) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}
func (m *mockTelegramServicer) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockTelegramServicer) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockTelegramServicer) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, nil
}
func (m *mockTelegramServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}
func (m *mockTelegramServicer) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (m *mockTelegramServicer) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (m *mockTelegramServicer) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}
func (m *mockTelegramServicer) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	return nil
}
func (m *mockTelegramServicer) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockTelegramServicer) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	return nil
}

func TestSchedulerPlugin(t *testing.T) {
	mockSched := newMockSchedulerService()
	p := New(mockSched)

	if p.Name() != "scheduler" {
		t.Errorf("expected name 'scheduler', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 5 {
		t.Fatalf("expected 5 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	mockSvc := &mockTelegramServicer{}
	peer := &tg.InputPeerChat{ChatID: 777}

	ctxRemindNoArgs := &core.Context{Ctx: context.Background(), Command: "remind", Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777}}
	if err := cmdMap["remind"].Handler(ctxRemindNoArgs); err == nil {
		t.Errorf("expected error for remind with no args")
	}

	ctxRemind := &core.Context{Ctx: context.Background(), Command: "remind", Args: []string{"15m", "drink", "water"}, RawArgs: "15m drink water", Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777}}
	if err := cmdMap["remind"].Handler(ctxRemind); err != nil {
		t.Fatalf("remind failed: %v", err)
	}
	if !strings.Contains(mockSvc.sent, "Reminder set!") {
		t.Errorf("expected reminder set confirmation, got: %s", mockSvc.sent)
	}

	ctxRemindReply := &core.Context{Ctx: context.Background(), Command: "remind", Args: []string{"10m"}, RawArgs: "10m", Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777}, Message: &core.Message{ReplyToID: 99}}
	if err := cmdMap["remind"].Handler(ctxRemindReply); err != nil {
		t.Fatalf("remind with reply failed: %v", err)
	}

	ctxSchedOnce := &core.Context{Ctx: context.Background(), Command: "schedule", Args: []string{"30m", ".whois"}, RawArgs: "30m .whois", Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777}, Message: &core.Message{SenderID: 42}}
	if err := cmdMap["schedule"].Handler(ctxSchedOnce); err != nil {
		t.Fatalf("schedule once failed: %v", err)
	}

	ctxList := &core.Context{Ctx: context.Background(), Command: "schedules", Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777}}
	if err := cmdMap["schedules"].Handler(ctxList); err != nil {
		t.Fatalf("schedules list failed: %v", err)
	}

	ctxCancel := &core.Context{
		Ctx: context.Background(), Command: "cancelschedule", Args: []string{"1"}, RawArgs: "1",
		Svc: mockSvc, PeerID: peer, Chat: &core.Chat{ID: 777},
		Sender: &core.User{ID: 1},
	}
	// Create a job owned by user 1 so CancelScoped succeeds.
	mockSched.jobs[1].CreatedBy = 1
	mockSched.jobs[1].ChatID = 777
	if err := cmdMap["cancelschedule"].Handler(ctxCancel); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
}
