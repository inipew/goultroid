package afk

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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

// Use the production manager/dispatcher and shared TaskEngine rather than a
// synthetic worker: disable must cancel only the old generation's FloodWait.
func TestA7BAFKManagedConcurrentAutoUnAFKAndGenerationIsolatedWelcome(t *testing.T) {
	const ownerID int64 = 1001
	ctx := context.Background()
	baseline := runtime.NumGoroutine()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	engine := taskengine.NewEngine(taskengine.NewDefaultConfig())
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := engine.Stop(stopCtx); err != nil {
			t.Error(err)
		}
		t.Logf("A7-B goroutines baseline=%d after-stop=%d", baseline, runtime.NumGoroutine())
	}()

	svc := &a2bBlockedWelcomeService{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-svc.release:
		default:
			close(svc.release)
		}
	}()
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
		t.Fatal("first AFK lifecycle has no managed scope")
	}
	if err := p.enableAFK(ctx, "away"); err != nil {
		t.Fatal(err)
	}

	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
	const burst = 32
	var wg sync.WaitGroup
	errs := make(chan error, burst)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			errs <- dispatcher.OnNewMessage(ctx, entities, &tg.UpdateNewMessage{Message: &tg.Message{
				ID: 100 + id, Out: true, FromID: &tg.PeerUser{UserID: ownerID},
				PeerID: &tg.PeerUser{UserID: 2002}, Message: fmt.Sprintf("manual %d", id),
			}})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent outgoing decision failed: %v", err)
		}
	}
	select {
	case <-svc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("auto-unAFK committed but welcome did not enter TaskEngine")
	}
	status, err := repo.GetAFK(ctx, ownerID)
	if err != nil || status == nil || status.IsAFK {
		t.Fatalf("auto-unAFK must persist before blocked Telegram welcome: %+v %v", status, err)
	}
	if p.state.Load().isAFK {
		t.Fatal("in-memory AFK remained active after outgoing transition")
	}
	svc.mu.Lock()
	alreadySent := len(svc.sentMessages)
	svc.mu.Unlock()
	if alreadySent != 0 {
		t.Fatalf("blocked FloodWait welcome sent inline: %d", alreadySent)
	}

	disableCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := mgr.Disable(disableCtx, "afk"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-svc.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("old-generation blocked welcome was not canceled")
	}
	if oldScope.ActiveGoroutines() != 0 {
		t.Fatalf("orphaned AFK old-generation goroutines: %d", oldScope.ActiveGoroutines())
	}
	close(svc.release) // after old effect is known canceled; new effect may complete.

	if err := mgr.Enable(ctx, "afk"); err != nil {
		t.Fatal(err)
	}
	newScope, ok := mgr.Scope("afk")
	if !ok || newScope == nil || newScope.Generation() == oldScope.Generation() {
		t.Fatalf("reload did not create a distinct scope: old=%v new=%v", oldScope, newScope)
	}
	if err := p.enableAFK(ctx, "back online"); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.OnNewMessage(ctx, entities, &tg.UpdateNewMessage{Message: &tg.Message{
		ID: 1001, Out: true, FromID: &tg.PeerUser{UserID: ownerID},
		PeerID: &tg.PeerUser{UserID: 2002}, Message: "second manual return",
	}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		svc.mu.Lock()
		count := len(svc.sentMessages)
		content := strings.Join(svc.sentMessages, "\n")
		svc.mu.Unlock()
		if count >= 1 {
			if count != 1 || !strings.Contains(content, "Welcome back") {
				t.Fatalf("duplicate or missing new-generation welcome: %d %q", count, content)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("new-generation welcome was incorrectly canceled with old scope")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if status, err := repo.GetAFK(ctx, ownerID); err != nil || status == nil || status.IsAFK {
		t.Fatalf("new-generation auto-unAFK did not persist: %+v %v", status, err)
	}
}

func TestA7BAFKSQLiteFailureKeepsActiveStateAndTaskRejectionHasNoFallback(t *testing.T) {
	const ownerID int64 = 1001
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	svc := &mockService{}
	p := New(repo, ownerID, func() core.TelegramServicer { return svc })
	client := &afkEffectTestClient{reject: errors.New("TaskEngine admission limit")}
	p.SetTaskClient(client)
	if err := p.enableAFK(ctx, "away"); err != nil {
		t.Fatal(err)
	}
	// Real SQLite failure injection, no mocked repository shortcuts.
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER deny_afk_deactivate
		BEFORE UPDATE OF is_afk ON afk_status
		WHEN NEW.is_afk = 0
		BEGIN SELECT RAISE(FAIL, 'injected SQLite failure'); END`); err != nil {
		t.Fatal(err)
	}
	entities := tg.Entities{Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 123}}}
	send := func(id int) {
		t.Helper()
		err := handleMessageEvent(p, ctx, entities, &tg.Message{
			ID: id, Out: true, FromID: &tg.PeerUser{UserID: ownerID},
			PeerID: &tg.PeerUser{UserID: 2002}, Message: "outgoing",
		}, false, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	send(77)
	if current, err := repo.GetAFK(ctx, ownerID); err != nil || current == nil || !current.IsAFK {
		t.Fatalf("failed durable deactivate committed unexpectedly: %+v %v", current, err)
	}
	if !p.state.Load().isAFK || len(client.snapshot()) != 0 {
		t.Fatal("SQLite failure mutated in-memory state or admitted welcome")
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER deny_afk_deactivate"); err != nil {
		t.Fatal(err)
	}
	send(78)
	if current, err := repo.GetAFK(ctx, ownerID); err != nil || current == nil || current.IsAFK {
		t.Fatalf("successful commit lost under rejected TaskEngine submission: %+v %v", current, err)
	}
	if p.state.Load().isAFK || len(client.snapshot()) != 0 {
		t.Fatal("rejected welcome admission undid deactivation or queued unmanaged task")
	}
	svc.mu.Lock()
	sends := len(svc.sentMessages)
	svc.mu.Unlock()
	if sends != 0 {
		t.Fatalf("rejected TaskEngine admission synchronously sent %d welcomes", sends)
	}
	restarted := New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := restarted.InitContext(ctx); err != nil || restarted.state.Load().isAFK {
		t.Fatalf("reloaded AFK state inconsistent: %v", err)
	}
}

func TestA7BAFKSQLiteInactiveFallbackPropagatesWriteFailure(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewSQLiteRepository(db)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER deny_afk_insert
		BEFORE INSERT ON afk_status
		BEGIN SELECT RAISE(FAIL, 'injected AFK insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetAFK(ctx, 2099, false, ""); err == nil {
		t.Fatal("SQLite inactive fallback swallowed a durable insert failure")
	}
	if persisted, err := repo.GetAFK(ctx, 2099); err != nil || persisted != nil {
		t.Fatalf("failed SQLite insert fabricated durable AFK row: %+v %v", persisted, err)
	}
}
