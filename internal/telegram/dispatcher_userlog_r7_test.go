package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	userlogSvc "github.com/inipew/goultroid/internal/services/userlog"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
	userlogPlugin "github.com/inipew/goultroid/plugins/userlog"
	"go.uber.org/zap"
)

type r7BlockingUserLogService struct {
	core.MockTelegramServicer
	started chan struct{}
	release chan struct{}
}

func (s *r7BlockingUserLogService) SendMessage(
	ctx context.Context,
	_ tg.InputPeerClass,
	_ string,
) (*tg.Message, error) {
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	select {
	case <-s.release:
		return nil, core.NewRateLimitError(30*time.Second, errors.New("FLOOD_WAIT_30"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestR7BlockedUserLogDeliveryDoesNotBlockDecisionLane(t *testing.T) {
	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	cfg := taskengine.NewDefaultConfig()
	general := cfg.Pools[tasks.PoolID("general")]
	general.Concurrency = 1
	general.MinConcurrency = 0
	general.ZeroIdle = true
	cfg.Pools[tasks.PoolID("general")] = general
	engine := taskengine.NewEngine(cfg)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)

	transport := &r7BlockingUserLogService{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	dispatcher.SetService(transport)
	repo := userlogSvc.NewSQLiteRepository(db)
	logSvc := userlogSvc.NewService(repo, transport, zap.NewNop())
	if err := logSvc.SetLogChat(context.Background(), 777); err != nil {
		t.Fatal(err)
	}

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	p := userlogPlugin.New(logSvc, ownerID)
	if err := mgr.Register(p); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Disable(context.Background(), "userlog") }()

	incomingEntities := tg.Entities{Users: map[int64]*tg.User{
		2002: {ID: 2002, AccessHash: 123, FirstName: "Alice"},
	}}
	if err := dispatcher.OnNewMessage(context.Background(), incomingEntities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      8101,
			Message: "hello",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("UserLog delivery did not enter blocked Telegram path")
	}

	decisionStarted := make(chan struct{}, 1)
	cleanup, err := dispatcher.RegisterMessageHook(core.MessageHookRegistration{
		Scope:    tasks.ScopeIdentity{Owner: "plugin:r7-decision", Generation: 1},
		Priority: PriorityFeature,
		Routing: core.MessageHookRouting{
			Lane: core.MessageHookDecision,
			Interests: []core.MessageHookInterest{{
				Directions: core.MessageDirectionOutgoing,
				Peers:      core.MessagePeerStable,
			}},
		},
		Execution: core.MessageHookExecutionPolicy{
			Ordering: core.MessageHookOrderingChat,
		},
		Handler: func(context.Context, *core.MessageEnvelope) error {
			decisionStarted <- struct{}{}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      8102,
			Out:     true,
			Message: "manual owner message",
			PeerID:  &tg.PeerUser{UserID: 3003},
			FromID:  &tg.PeerUser{UserID: ownerID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-decisionStarted:
	case <-time.After(time.Second):
		t.Fatal("blocked UserLog observability RPC held interactive decision lane")
	}

	close(transport.release)
}
