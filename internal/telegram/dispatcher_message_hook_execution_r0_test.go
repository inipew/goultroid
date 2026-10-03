package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/afk"
	"go.uber.org/zap"
)

type r0ImmediateTicket struct {
	id   tasks.TaskID
	done chan struct{}

	mu     sync.Mutex
	result tasks.TaskResult
}

func (t *r0ImmediateTicket) TaskID() tasks.TaskID { return t.id }
func (t *r0ImmediateTicket) State() tasks.TaskState {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		if t.result.IsSuccess() {
			return tasks.StateCompleted
		}
		return tasks.StateFailed
	default:
		return tasks.StateRunning
	}
}
func (t *r0ImmediateTicket) Done() <-chan struct{} { return t.done }
func (t *r0ImmediateTicket) Result() (tasks.TaskResult, bool) {
	select {
	case <-t.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.result, true
	default:
		return tasks.TaskResult{}, false
	}
}
func (t *r0ImmediateTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	select {
	case <-t.done:
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.result, nil
	case <-ctx.Done():
		return tasks.TaskResult{}, ctx.Err()
	}
}

func (t *r0ImmediateTicket) complete(result tasks.TaskResult) {
	t.mu.Lock()
	t.result = result
	t.mu.Unlock()
	close(t.done)
}

type r0RecordingTaskClient struct {
	mu      sync.Mutex
	specs   []tasks.WorkSpec
	runWork bool
}

func (c *r0RecordingTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.specs = append(c.specs, spec)
	c.mu.Unlock()

	ticket := &r0ImmediateTicket{id: spec.ID, done: make(chan struct{})}
	run := func() {
		result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
		if c.runWork && spec.Handler != nil {
			if err := spec.Handler(ctx); err != nil {
				result.Outcome = tasks.OutcomeFailed
				result.Failure.Message = err.Error()
			}
		}
		if spec.OnComplete != nil {
			spec.OnComplete(result)
		}
		ticket.complete(result)
	}
	if strings.HasPrefix(string(spec.ID), "cmd:") {
		go run()
	} else {
		run()
	}
	return ticket, nil
}

func (*r0RecordingTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}

func (*r0RecordingTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }

func (*r0RecordingTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func (c *r0RecordingTaskClient) snapshot() []tasks.WorkSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]tasks.WorkSpec, len(c.specs))
	copy(out, c.specs)
	return out
}

func (c *r0RecordingTaskClient) reset() {
	c.mu.Lock()
	c.specs = nil
	c.mu.Unlock()
}

func TestR0InactiveAFKStillAdmitsDecisionTask(t *testing.T) {
	const ownerID int64 = 1001
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{runWork: true}
	dispatcher.SetTasks(tasksClient)
	dispatcher.SetSelfID(ownerID)

	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	afkPlugin := afk.New(nil, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(afkPlugin); err != nil {
		t.Fatalf("register AFK plugin: %v", err)
	}
	defer func() { _ = mgr.Disable(context.Background(), "afk") }()

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      101,
			Out:     true,
			Message: "ordinary outgoing message while AFK is inactive",
			PeerID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatalf("dispatch outgoing message: %v", err)
	}

	specs := tasksClient.snapshot()
	if len(specs) != 1 {
		t.Fatalf("TaskEngine submissions=%d, want exactly one AFK decision admission", len(specs))
	}
	spec := specs[0]
	if !strings.HasPrefix(string(spec.ID), "decision:") {
		t.Fatalf("task id=%q, want decision task", spec.ID)
	}
	if spec.Scope.Owner != "plugin:afk" || spec.QuotaOwner != tasks.OwnerID("plugin:afk") {
		t.Fatalf("AFK task ownership mismatch: scope=%+v quota=%q", spec.Scope, spec.QuotaOwner)
	}
	if spec.Pool != tasks.PoolID("interactive") || spec.Class != tasks.PriorityInteractive {
		t.Fatalf("AFK inactive task entered unexpected lane: pool=%q class=%q", spec.Pool, spec.Class)
	}
	if spec.OrderingKey != "msg-decision:chat:2002" {
		t.Fatalf("AFK inactive ordering key=%q, want decision-domain chat key", spec.OrderingKey)
	}
}

func TestR1DecisionAndEventTasksUseDistinctOrderingDomains(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{runWork: true}
	dispatcher.SetTasks(tasksClient)

	msg := &tg.Message{ID: 202, PeerID: &tg.PeerChat{ChatID: 42}, Message: "hello"}
	envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 1)
	decision := prioritizedHandler{
		id:               1,
		priority:         PriorityFeature,
		failurePolicy:    FailurePolicyFailOpen,
		routing:          core.MessageHookRouting{Lane: core.MessageHookDecision},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error { return nil },
		scope:            tasks.ScopeIdentity{Owner: "plugin:r0-decision", Generation: 1},
	}
	event := prioritizedHandler{
		id:               2,
		priority:         PriorityFeature,
		failurePolicy:    FailurePolicyFailOpen,
		routing:          core.MessageHookRouting{Lane: core.MessageHookEvent},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error { return nil },
		scope:            tasks.ScopeIdentity{Owner: "plugin:r0-event", Generation: 1},
	}

	if handled := dispatcher.executeDecisionHandlersEnvelope(
		context.Background(),
		[]prioritizedHandler{decision},
		envelope,
		tg.Entities{},
		msg,
	); handled {
		t.Fatal("no-op R0 decision handler unexpectedly handled the message")
	}
	dispatcher.dispatchEventHandlersEnvelope(
		context.Background(),
		[]prioritizedHandler{event},
		envelope,
		tg.Entities{},
		msg,
	)

	specs := tasksClient.snapshot()
	if len(specs) != 2 {
		t.Fatalf("TaskEngine submissions=%d, want decision + event", len(specs))
	}
	if specs[0].Pool != tasks.PoolID("interactive") || specs[1].Pool != tasks.PoolID("general") {
		t.Fatalf("unexpected pools: decision=%q event=%q", specs[0].Pool, specs[1].Pool)
	}
	if specs[0].OrderingKey != "msg-decision:chat:42" {
		t.Fatalf("decision ordering key=%q, want msg-decision:chat:42", specs[0].OrderingKey)
	}
	if specs[1].OrderingKey != "msg-event:plugin:r0-event:chat:42" {
		t.Fatalf("event ordering key=%q, want plugin-scoped event domain", specs[1].OrderingKey)
	}
	if specs[0].OrderingKey == specs[1].OrderingKey {
		t.Fatalf("decision and event ordering domains still collide: %q", specs[0].OrderingKey)
	}
}

func TestR0DecisionOrderingIsChatScopedAcrossUpdates(t *testing.T) {
	dispatcher := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{runWork: true}
	dispatcher.SetTasks(tasksClient)

	handler := prioritizedHandler{
		id:               3,
		priority:         PriorityFeature,
		failurePolicy:    FailurePolicyFailOpen,
		routing:          core.MessageHookRouting{Lane: core.MessageHookDecision},
		canonicalHandler: func(context.Context, *core.MessageEnvelope) error { return nil },
		scope:            tasks.ScopeIdentity{Owner: "plugin:r0-owner-global", Generation: 1},
	}
	for i, chatID := range []int64{41, 42} {
		msg := &tg.Message{ID: 300 + i, PeerID: &tg.PeerChat{ChatID: chatID}, Message: "hello"}
		envelope := NormalizeMessageEnvelope(tg.Entities{}, msg, false, "", 1)
		if handled := dispatcher.executeDecisionHandlersEnvelope(
			context.Background(),
			[]prioritizedHandler{handler},
			envelope,
			tg.Entities{},
			msg,
		); handled {
			t.Fatalf("chat %d no-op decision unexpectedly handled message", chatID)
		}
	}

	specs := tasksClient.snapshot()
	if len(specs) != 2 {
		t.Fatalf("TaskEngine submissions=%d, want 2", len(specs))
	}
	if specs[0].OrderingKey != "msg-decision:chat:41" || specs[1].OrderingKey != "msg-decision:chat:42" {
		t.Fatalf("unexpected chat-scoped ordering keys: %q, %q", specs[0].OrderingKey, specs[1].OrderingKey)
	}
	if specs[0].OrderingKey == specs[1].OrderingKey {
		t.Fatal("R0 reproducer requires current per-chat domains to differ across chats")
	}
}

type r0BlockingRateLimitedWelcomeService struct {
	afkTestService
	welcomeStarted chan struct{}
	releaseWelcome chan struct{}
	startOnce      sync.Once
}

func (s *r0BlockingRateLimitedWelcomeService) SendMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	if !strings.Contains(text, "Welcome back") {
		return s.afkTestService.SendMessage(ctx, peer, text)
	}
	s.startOnce.Do(func() { close(s.welcomeStarted) })
	select {
	case <-s.releaseWelcome:
		return nil, core.NewRateLimitError(30*time.Second, errors.New("FLOOD_WAIT_30"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestR0AFKCommandStartWaitsForWelcomeRateLimitPath(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{runWork: true}
	dispatcher.SetTasks(tasksClient)
	dispatcher.SetSelfID(ownerID)

	svc := &r0BlockingRateLimitedWelcomeService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		welcomeStarted: make(chan struct{}),
		releaseWelcome: make(chan struct{}),
	}
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	repo := afk.NewSQLiteRepository(db)
	afkPlugin := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(afkPlugin); err != nil {
		t.Fatalf("register AFK plugin: %v", err)
	}
	defer func() {
		select {
		case <-svc.releaseWelcome:
		default:
			close(svc.releaseWelcome)
		}
		_ = mgr.Disable(context.Background(), "afk")
	}()

	commandStarted := make(chan struct{})
	if err := router.Register(core.Command{
		Name:       "r0probe",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			close(commandStarted)
			return nil
		},
	}); err != nil {
		t.Fatalf("register probe command: %v", err)
	}

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      401,
			Out:     true,
			Message: ".afk sleeping",
			PeerID:  &tg.PeerUser{UserID: ownerID},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatalf("activate AFK: %v", err)
	}
	var state *afk.AFK
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state, err = repo.GetAFK(context.Background(), ownerID)
		if err == nil && state != nil && state.IsAFK {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || state == nil || !state.IsAFK {
		t.Fatalf("AFK activation state=%+v err=%v", state, err)
	}
	tasksClient.reset()

	dispatchDone := make(chan error, 1)
	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	go func() {
		dispatchDone <- dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      402,
				Out:     true,
				Message: ".r0probe",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		})
	}()

	select {
	case <-svc.welcomeStarted:
	case <-time.After(time.Second):
		t.Fatal("AFK welcome did not reach the simulated rate-limit path")
	}

	state, err = repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK must already be inactive while welcome is blocked: state=%+v err=%v", state, err)
	}
	select {
	case <-commandStarted:
		t.Fatal("command started before the synchronous AFK welcome path returned")
	case <-time.After(100 * time.Millisecond):
	}

	close(svc.releaseWelcome)
	select {
	case err := <-dispatchDone:
		if err != nil {
			t.Fatalf("dispatch probe command: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not continue after rate-limited welcome returned")
	}
	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("probe command did not start after AFK welcome path returned")
	}

	specs := tasksClient.snapshot()
	if len(specs) != 2 {
		t.Fatalf("TaskEngine submissions=%d, want AFK decision + command", len(specs))
	}
	if !strings.HasPrefix(string(specs[0].ID), "decision:") || !strings.HasPrefix(string(specs[1].ID), "cmd:") {
		t.Fatalf("unexpected submission order: %q then %q", specs[0].ID, specs[1].ID)
	}
}
