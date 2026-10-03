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
	"github.com/inipew/goultroid/internal/taskengine"
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

func TestR3InactiveAFKSkipsDecisionBeforeTaskAdmission(t *testing.T) {
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
	dispatcher.SetService(svc)
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(tasksClient)
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

	if specs := tasksClient.snapshot(); len(specs) != 0 {
		t.Fatalf("inactive AFK TaskEngine submissions=%d, want 0: %+v", len(specs), specs)
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

type r4BlockingRateLimitedWelcomeService struct {
	afkTestService
	welcomeStarted  chan struct{}
	welcomeCanceled chan struct{}
	releaseWelcome  chan struct{}
	startOnce       sync.Once
	cancelOnce      sync.Once
}

func (s *r4BlockingRateLimitedWelcomeService) SendMessage(
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
		s.cancelOnce.Do(func() { close(s.welcomeCanceled) })
		return nil, ctx.Err()
	}
}

func TestR4AFKCommandStartsBeforeWelcomeRateLimitCompletes(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := taskengine.NewEngine(taskengine.DefaultConfig)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)

	svc := &r4BlockingRateLimitedWelcomeService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		welcomeStarted:  make(chan struct{}),
		welcomeCanceled: make(chan struct{}),
		releaseWelcome:  make(chan struct{}),
	}
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
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

	commandStarted := make(chan bool, 1)
	if err := router.Register(core.Command{
		Name:       "r4probe",
		Permission: core.PermissionOwner,
		Handler: func(ctx *core.Context) error {
			state, stateErr := repo.GetAFK(ctx.Ctx, ownerID)
			commandStarted <- stateErr == nil && state != nil && !state.IsAFK
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

	dispatchDone := make(chan error, 1)
	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	go func() {
		dispatchDone <- dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      402,
				Out:     true,
				Message: ".r4probe",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		})
	}()

	select {
	case <-svc.welcomeStarted:
	case <-time.After(time.Second):
		t.Fatal("asynchronous AFK welcome did not start")
	}

	select {
	case inactive := <-commandStarted:
		if !inactive {
			t.Fatal("command started before AFK inactive state was committed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("command start remained blocked on welcome/rate-limit effect")
	}

	select {
	case err := <-dispatchDone:
		if err != nil {
			t.Fatalf("dispatch probe command: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not return while welcome effect was blocked")
	}

	state, err = repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK state regressed while welcome was blocked: state=%+v err=%v", state, err)
	}

	close(svc.releaseWelcome)
	time.Sleep(25 * time.Millisecond)
	state, err = repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("welcome failure rolled AFK back active: state=%+v err=%v", state, err)
	}
}

func TestR4AFKTransitionOrderingIsPluginGlobalAcrossChats(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "ordering"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{}
	dispatcher.SetTasks(tasksClient)
	dispatcher.SetSelfID(ownerID)

	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	dispatcher.SetService(svc)
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(tasksClient)
	p := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Disable(context.Background(), "afk") }()

	for i, chatID := range []int64{41, 42} {
		if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      500 + i,
				Out:     true,
				Message: "manual",
				PeerID:  &tg.PeerChat{ChatID: chatID},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	specs := tasksClient.snapshot()
	if len(specs) != 2 {
		t.Fatalf("AFK decision submissions=%d, want 2", len(specs))
	}
	if specs[0].OrderingKey != "msg-decision:plugin:afk" ||
		specs[1].OrderingKey != "msg-decision:plugin:afk" {
		t.Fatalf("AFK owner-global ordering keys=%q, %q", specs[0].OrderingKey, specs[1].OrderingKey)
	}
}



func TestR4AFKDisableCancelsScopedWelcomeEffect(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "reload"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := taskengine.NewEngine(taskengine.DefaultConfig)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)

	svc := &r4BlockingRateLimitedWelcomeService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		welcomeStarted:  make(chan struct{}),
		welcomeCanceled: make(chan struct{}),
		releaseWelcome:  make(chan struct{}),
	}
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	p := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	oldScope, ok := mgr.Scope("afk")
	if !ok || oldScope == nil {
		t.Fatal("AFK scope missing after registration")
	}
	oldGeneration := oldScope.Generation()

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{
		Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}},
	}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      601,
			Out:     true,
			Message: "manual return",
			PeerID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-svc.welcomeStarted:
	case <-time.After(time.Second):
		t.Fatal("scoped welcome effect did not start")
	}

	disableCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mgr.Disable(disableCtx, "afk"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-svc.welcomeCanceled:
	case <-time.After(time.Second):
		t.Fatal("plugin disable did not cancel stale welcome effect")
	}

	if err := mgr.Enable(context.Background(), "afk"); err != nil {
		t.Fatal(err)
	}
	newScope, ok := mgr.Scope("afk")
	if !ok || newScope == nil {
		t.Fatal("AFK scope missing after re-enable")
	}
	if newScope.Generation() == 0 || newScope.Generation() == oldGeneration {
		t.Fatalf(
			"AFK re-enable generation=%d, want new non-zero generation distinct from %d",
			newScope.Generation(),
			oldGeneration,
		)
	}
	defer func() { _ = mgr.Disable(context.Background(), "afk") }()

	select {
	case <-svc.releaseWelcome:
	default:
		close(svc.releaseWelcome)
	}
}
