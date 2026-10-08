package afk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

type a2bBlockedWelcomeService struct {
	mockService
	started    chan struct{}
	canceled   chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func (s *a2bBlockedWelcomeService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if !strings.Contains(text, "Welcome back") {
		return s.mockService.SendMessage(ctx, peer, text)
	}
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		s.cancelOnce.Do(func() { close(s.canceled) })
		return nil, ctx.Err()
	case <-s.release:
		return s.mockService.SendMessage(ctx, peer, text)
	}
}

func TestA2BManagedWelcomeDoesNotDelayCommandsAndCancelsOnReload(t *testing.T) {
	const ownerID int64 = 1001
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := engine.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	}()

	svc := &a2bBlockedWelcomeService{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	defer func() { close(svc.release) }()
	repo := NewSQLiteRepository(db)
	router := core.NewRouter(".")
	dispatcher := telegram.NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	gate := plugin.NewCapabilityGate()
	gate.SetFailClosed(true)
	mgr.SetPlatformServices(gate, nil, nil, nil, nil, nil)
	mgr.SetTaskClient(engine)
	mgr.SetHookRegistrar(dispatcher)
	p := New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.RegisterModule(ctx, Module.Manifest(), p); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mgr.Disable(context.Background(), "afk"); err != nil {
			t.Error(err)
		}
	}()
	oldScope, ok := mgr.Scope("afk")
	if !ok || oldScope == nil {
		t.Fatal("registered AFK scope missing")
	}

	commandStarted := make(chan error, 1)
	if err := router.Register(core.Command{
		Name: "a2bprobe", Permission: core.PermissionOwner,
		Handler: func(commandCtx *core.Context) error {
			state, err := repo.GetAFK(commandCtx.Ctx, ownerID)
			if err == nil && (state == nil || state.IsAFK) {
				err = fmt.Errorf("command started before AFK deactivation committed")
			}
			commandStarted <- err
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.enableAFK(ctx, "meeting"); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
	if err := dispatcher.OnNewMessage(ctx, entities, &tg.UpdateNewMessage{Message: &tg.Message{
		ID: 501, Out: true, FromID: &tg.PeerUser{UserID: ownerID}, PeerID: &tg.PeerUser{UserID: 2002}, Message: "manual return",
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-svc.started:
	case <-time.After(3 * time.Second):
		t.Fatal("AFK welcome effect did not start")
	}

	if err := dispatcher.OnNewMessage(ctx, entities, &tg.UpdateNewMessage{Message: &tg.Message{
		ID: 502, Out: true, FromID: &tg.PeerUser{UserID: ownerID}, PeerID: &tg.PeerUser{UserID: 2002}, Message: ".a2bprobe",
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-commandStarted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command was blocked by AFK welcome Telegram RPC")
	}
	select {
	case <-svc.canceled:
		t.Fatal("welcome canceled before plugin disable")
	default:
	}

	disableCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := mgr.Disable(disableCtx, "afk"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-svc.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin disable did not cancel in-flight welcome")
	}
	if err := mgr.Enable(ctx, "afk"); err != nil {
		t.Fatal(err)
	}
	newScope, ok := mgr.Scope("afk")
	if !ok || newScope == nil || newScope.Generation() == oldScope.Generation() {
		t.Fatalf("reload did not create a new plugin generation: old=%v new=%v", oldScope, newScope)
	}
	state, err := repo.GetAFK(ctx, ownerID)
	if err != nil || state == nil || state.IsAFK {
		t.Fatalf("AFK was restored after canceled welcome: state=%+v err=%v", state, err)
	}
}
