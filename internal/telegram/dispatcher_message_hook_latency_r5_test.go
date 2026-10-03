package telegram

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/plugins/afk"
	"go.uber.org/zap"
)

func r5StartTaskEngine(t *testing.T, dispatcher *Dispatcher, cfg taskengine.Config) *taskengine.Engine {
	t.Helper()
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	dispatcher.SetTasks(engine)
	t.Cleanup(func() { _ = engine.Stop(context.Background()) })
	return engine
}

func r5RegisterAFK(
	t *testing.T,
	router *core.Router,
	dispatcher *Dispatcher,
	engine *taskengine.Engine,
	repo afk.Repository,
	ownerID int64,
	svc core.TelegramServicer,
) (*plugin.Manager, *afk.Plugin) {
	t.Helper()
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	p := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register AFK plugin: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Disable(context.Background(), "afk") })
	return mgr, p
}

type r5BlockingSleeper struct {
	started chan time.Duration
	release chan struct{}
}

func (s *r5BlockingSleeper) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case s.started <- d:
	default:
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type r5ExecutorWelcomeService struct {
	afkTestService
	executor       *RPCExecutor
	operation      func(context.Context) error
	welcomeDone    chan error
	operationCalls atomic.Int32
}

func (s *r5ExecutorWelcomeService) SendMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	if !strings.Contains(text, "Welcome back") {
		return s.afkTestService.SendMessage(ctx, peer, text)
	}
	err := s.executor.Do(ctx, RPCMeta{
		Method:       "messages.sendMessage",
		Family:       "messages",
		PeerLimitKey: LimitKey{Scope: "peer", Key: "r5-welcome"},
		Kind:         RPCNonIdempotentMutation,
		RetryPolicy: RetryPolicy{
			MaxAttempts:        1,
			MaxElapsed:         15 * time.Second,
			InlineFloodWaitMax: 5 * time.Second,
		},
	}, func(opCtx context.Context) error {
		s.operationCalls.Add(1)
		return s.operation(opCtx)
	})
	if s.welcomeDone != nil {
		select {
		case s.welcomeDone <- err:
		default:
		}
	}
	if err != nil {
		return nil, err
	}
	return s.afkTestService.SendMessage(ctx, peer, text)
}

func TestR5WelcomeShortLimiterWaitStaysOffCommandBarrier(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "short limiter wait"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5StartTaskEngine(t, dispatcher, taskengine.NewDefaultConfig())
	dispatcher.SetSelfID(ownerID)

	limiter := &oneWaitLimiter{wait: 25 * time.Millisecond}
	sleeper := &r5BlockingSleeper{
		started: make(chan time.Duration, 1),
		release: make(chan struct{}),
	}
	rpcExecutor := newTestExecutor(limiter, NewFakeClock(time.Now()), sleeper, nil)
	svc := &r5ExecutorWelcomeService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		executor:    rpcExecutor,
		operation:   func(context.Context) error { return nil },
		welcomeDone: make(chan error, 1),
	}
	dispatcher.SetService(svc)
	_, p := r5RegisterAFK(t, router, dispatcher, engine, repo, ownerID, svc)

	commandStarted := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "r5short",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			commandStarted <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      7001,
			Out:     true,
			Message: ".r5short",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case wait := <-sleeper.started:
		if wait != 25*time.Millisecond {
			t.Fatalf("limiter wait=%s, want 25ms", wait)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome effect did not reach shared RPC limiter wait")
	}

	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("short welcome limiter wait held command-start barrier")
	}

	state, err := repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK transition was not committed before command start: state=%+v err=%v", state, err)
	}
	if p == nil {
		t.Fatal("AFK plugin missing")
	}

	close(sleeper.release)
	select {
	case err := <-svc.welcomeDone:
		if err != nil {
			t.Fatalf("welcome effect failed after limiter release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome effect did not complete after limiter release")
	}

	limiter.mu.Lock()
	reserves := limiter.reserves
	limiter.mu.Unlock()
	if reserves != 2 {
		t.Fatalf("shared limiter reserve calls=%d, want deny+re-reserve", reserves)
	}
}

func TestR5WelcomeServerFloodWaitPenalizesSharedLimiterWithoutHoldingCommandStart(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "server flood wait"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5StartTaskEngine(t, dispatcher, taskengine.NewDefaultConfig())
	dispatcher.SetSelfID(ownerID)

	limiter := &fakeLimiter{}
	sleeper := &FakeSleeper{}
	rpcExecutor := newTestExecutor(limiter, NewFakeClock(time.Now()), sleeper, nil)
	svc := &r5ExecutorWelcomeService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		executor: rpcExecutor,
		operation: func(context.Context) error {
			return tgerr.New(420, "FLOOD_WAIT_30")
		},
		welcomeDone: make(chan error, 1),
	}
	dispatcher.SetService(svc)
	r5RegisterAFK(t, router, dispatcher, engine, repo, ownerID, svc)

	commandStarted := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "r5flood",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			commandStarted <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      7002,
			Out:     true,
			Message: ".r5flood",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("server FloodWait on welcome held command-start barrier")
	}

	select {
	case welcomeErr := <-svc.welcomeDone:
		if welcomeErr == nil || !errors.Is(welcomeErr, core.ErrRateLimit) {
			t.Fatalf("welcome FloodWait did not surface structured rate-limit error: %v", welcomeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("welcome FloodWait result not observed")
	}

	if got := svc.operationCalls.Load(); got != 1 {
		t.Fatalf("physical welcome RPC calls=%d, want exactly 1", got)
	}
	if sleeper.Calls() != 0 {
		t.Fatalf("above-threshold server FloodWait slept inline %d time(s)", sleeper.Calls())
	}
	limiter.mu.Lock()
	penalties := append([]time.Duration(nil), limiter.penalized...)
	limiter.mu.Unlock()
	if len(penalties) != 1 || penalties[0] != 30*time.Second {
		t.Fatalf("shared limiter penalties=%v, want [30s]", penalties)
	}
	state, err := repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("welcome FloodWait changed committed inactive state: state=%+v err=%v", state, err)
	}
}

func TestR5SQLiteContentionRemainsInsideRequiredTransitionBarrier(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(filepath.Join(t.TempDir(), "r5-contention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "sqlite contention"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5StartTaskEngine(t, dispatcher, taskengine.NewDefaultConfig())
	dispatcher.SetSelfID(ownerID)
	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	dispatcher.SetService(svc)
	r5RegisterAFK(t, router, dispatcher, engine, repo, ownerID, svc)

	commandStarted := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "r5db",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			commandStarted <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	dispatchDone := make(chan error, 1)
	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	go func() {
		dispatchDone <- dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      7003,
				Out:     true,
				Message: ".r5db",
				PeerID:  &tg.PeerUser{UserID: 2002},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		})
	}()

	deadline := time.Now().Add(time.Second)
	blockedTransitionObserved := false
	for time.Now().Before(deadline) {
		stats, statsErr := engine.Stats(context.Background())
		if statsErr == nil && stats.ActiveTasks > 0 {
			blockedTransitionObserved = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !blockedTransitionObserved {
		t.Fatal("AFK transition never became active under SQLite write contention")
	}
	select {
	case <-commandStarted:
		t.Fatal("command started before required AFK persistence completed")
	default:
	}

	if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	locked = false

	select {
	case <-commandStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("command did not start after SQLite contention was released")
	}
	select {
	case err := <-dispatchDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not finish after SQLite contention was released")
	}

	state, err := repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK state not durably inactive after contention release: state=%+v err=%v", state, err)
	}
}

func TestR5GeneralEventBacklogAndCrossPluginPressureDoNotBlockDecision(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := afk.NewSQLiteRepository(db)
	if err := repo.SetAFK(context.Background(), ownerID, true, "event backlog"); err != nil {
		t.Fatal(err)
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	cfg := taskengine.NewDefaultConfig()
	general := cfg.Pools[tasks.PoolID("general")]
	general.Concurrency = 2
	general.MinConcurrency = 0
	general.ZeroIdle = true
	cfg.Pools[tasks.PoolID("general")] = general
	engine := r5StartTaskEngine(t, dispatcher, cfg)
	dispatcher.SetSelfID(ownerID)

	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	dispatcher.SetService(svc)
	_, p := r5RegisterAFK(t, router, dispatcher, engine, repo, ownerID, svc)
	p.SetAutoReply(false)

	eventStarted := make(chan string, 2)
	releaseEvents := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseEvents) }) })

	for _, owner := range []string{"plugin:r5-event-a", "plugin:r5-event-b"} {
		owner := owner
		cleanup, err := dispatcher.RegisterMessageHook(core.MessageHookRegistration{
			Scope:    tasks.ScopeIdentity{Owner: owner, Generation: 1},
			Priority: PriorityObservability,
			Routing: core.MessageHookRouting{
				Lane: core.MessageHookEvent,
				Interests: []core.MessageHookInterest{{
					Directions: core.MessageDirectionIncoming,
					Peers:      core.MessagePeerPrivate,
				}},
			},
			Handler: func(ctx context.Context, _ *core.MessageEnvelope) error {
				eventStarted <- owner
				select {
				case <-releaseEvents:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      7004,
			Out:     false,
			Message: "fill general event workers",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case owner := <-eventStarted:
			seen[owner] = true
		case <-time.After(time.Second):
			t.Fatalf("same-chat cross-plugin events did not both start; seen=%v", seen)
		}
	}

	commandStarted := make(chan struct{}, 1)
	if err := router.Register(core.Command{
		Name:       "r5backlog",
		Permission: core.PermissionOwner,
		Handler: func(*core.Context) error {
			commandStarted <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      7005,
			Out:     true,
			Message: ".r5backlog",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("saturated general event pool blocked interactive AFK decision/command start")
	}

	state, err := repo.GetAFK(context.Background(), ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK transition did not commit under event backlog: state=%+v err=%v", state, err)
	}

	releaseOnce.Do(func() { close(releaseEvents) })
}

type r5BlockingTransitionRepository struct {
	base afk.Repository

	mu            sync.Mutex
	disableCalls  int
	activeDisable int
	maxActive     int

	firstDisableStarted chan struct{}
	releaseFirst        chan struct{}
	firstOnce           sync.Once
}

func (r *r5BlockingTransitionRepository) GetAFK(ctx context.Context, userID int64) (*afk.AFK, error) {
	return r.base.GetAFK(ctx, userID)
}

func (r *r5BlockingTransitionRepository) SetAFK(
	ctx context.Context,
	userID int64,
	isAFK bool,
	reason string,
) error {
	if isAFK {
		return r.base.SetAFK(ctx, userID, true, reason)
	}

	r.mu.Lock()
	r.disableCalls++
	call := r.disableCalls
	r.activeDisable++
	if r.activeDisable > r.maxActive {
		r.maxActive = r.activeDisable
	}
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.activeDisable--
		r.mu.Unlock()
	}()

	if call == 1 {
		r.firstOnce.Do(func() { close(r.firstDisableStarted) })
		select {
		case <-r.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.base.SetAFK(ctx, userID, false, reason)
}

func (r *r5BlockingTransitionRepository) snapshot() (disableCalls, maxActive int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disableCalls, r.maxActive
}

func TestR5SimultaneousTwoChatAFKTransitionProducesSingleWelcome(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := afk.NewSQLiteRepository(db)
	if err := base.SetAFK(context.Background(), ownerID, true, "two chats"); err != nil {
		t.Fatal(err)
	}
	repo := &r5BlockingTransitionRepository{
		base:                base,
		firstDisableStarted: make(chan struct{}),
		releaseFirst:        make(chan struct{}),
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5StartTaskEngine(t, dispatcher, taskengine.NewDefaultConfig())
	dispatcher.SetSelfID(ownerID)
	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	dispatcher.SetService(svc)
	r5RegisterAFK(t, router, dispatcher, engine, repo, ownerID, svc)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      7006,
				Out:     true,
				Message: "first chat",
				PeerID:  &tg.PeerChat{ChatID: 41},
			},
		})
	}()

	select {
	case <-repo.firstDisableStarted:
	case <-time.After(time.Second):
		t.Fatal("first AFK transition did not reach persistence")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      7007,
				Out:     true,
				Message: "second chat",
				PeerID:  &tg.PeerChat{ChatID: 42},
			},
		})
	}()

	deadline := time.Now().Add(time.Second)
	twoAdmitted := false
	for time.Now().Before(deadline) {
		stats, statsErr := engine.Stats(context.Background())
		if statsErr == nil && stats.ActiveTasks >= 2 {
			twoAdmitted = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !twoAdmitted {
		t.Fatal("two simultaneous AFK decisions were not both admitted")
	}

	close(repo.releaseFirst)

	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s dispatch failed: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s dispatch did not finish", name)
		}
	}

	disableCalls, maxActive := repo.snapshot()
	if disableCalls != 1 {
		t.Fatalf("AFK persistence disable calls=%d, want exactly 1", disableCalls)
	}
	if maxActive != 1 {
		t.Fatalf("concurrent AFK persistence transitions=%d, want 1", maxActive)
	}

	deadline = time.Now().Add(time.Second)
	welcomeCount := 0
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		welcomeCount = 0
		for _, text := range svc.sentMessages {
			if strings.Contains(text, "Welcome back") {
				welcomeCount++
			}
		}
		svc.mu.Unlock()
		if welcomeCount == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if welcomeCount != 1 {
		t.Fatalf("welcome count=%d, want exactly 1", welcomeCount)
	}
	time.Sleep(25 * time.Millisecond)
	svc.mu.Lock()
	welcomeCount = 0
	for _, text := range svc.sentMessages {
		if strings.Contains(text, "Welcome back") {
			welcomeCount++
		}
	}
	svc.mu.Unlock()
	if welcomeCount != 1 {
		t.Fatalf("late duplicate welcome count=%d, want exactly 1", welcomeCount)
	}
}

type r5BlockingCommandOutputService struct {
	afkTestService
	outputStarted chan struct{}
	releaseOutput chan struct{}
	startOnce     sync.Once
}

func (s *r5BlockingCommandOutputService) SendMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	if text != "r5-output" {
		return s.afkTestService.SendMessage(ctx, peer, text)
	}
	s.startOnce.Do(func() { close(s.outputStarted) })
	select {
	case <-s.releaseOutput:
		return s.afkTestService.SendMessage(ctx, peer, text)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestR5CommandStartAndOutputCompletionAreSeparateLatencyStages(t *testing.T) {
	const ownerID int64 = 1001
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := r5StartTaskEngine(t, dispatcher, taskengine.NewDefaultConfig())
	dispatcher.SetSelfID(ownerID)

	svc := &r5BlockingCommandOutputService{
		afkTestService: afkTestService{
			botSentIDs: make(map[int]bool),
			messages:   make(map[int]*tg.Message),
		},
		outputStarted: make(chan struct{}),
		releaseOutput: make(chan struct{}),
	}
	dispatcher.SetService(svc)
	r5RegisterAFK(t, router, dispatcher, engine, nil, ownerID, svc)

	commandStarted := make(chan time.Time, 1)
	handlerDone := make(chan error, 1)
	if err := router.Register(core.Command{
		Name:       "r5output",
		Permission: core.PermissionOwner,
		Handler: func(ctx *core.Context) error {
			commandStarted <- time.Now()
			err := ctx.Reply("r5-output")
			handlerDone <- err
			return err
		},
	}); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123},
	}}
	ingressAt := time.Now()
	if err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      7008,
			Out:     true,
			Message: ".r5output",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var startedAt time.Time
	select {
	case startedAt = <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("command handler did not start")
	}
	select {
	case <-svc.outputStarted:
	case <-time.After(time.Second):
		t.Fatal("command output RPC did not start")
	}
	select {
	case err := <-handlerDone:
		t.Fatalf("command output completed before controlled release: %v", err)
	default:
	}

	l1 := startedAt.Sub(ingressAt)
	close(svc.releaseOutput)
	var outputErr error
	select {
	case outputErr = <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("command output did not complete after release")
	}
	if outputErr != nil {
		t.Fatal(outputErr)
	}
	l2 := time.Since(startedAt)
	t.Logf("R5 latency stages: L1 ingress->handler-start=%s, L2 handler-start->output-complete=%s", l1, l2)
}
