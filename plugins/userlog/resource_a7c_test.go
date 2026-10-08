package userlog

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	userlogsvc "github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
)

// A7-C combines private and group-mention traffic with blocked observer I/O.
// The acceptance gates are bounded admission, cancellation and clean reload;
// portable heap and goroutine observations are reported, not hardcoded.
func TestA7CUserLogMixedChatBurstBoundedAndReloadSettles(t *testing.T) {
	ctx := context.Background()
	startGoroutines := runtime.NumGoroutine()
	var baseline, peak, settled runtime.MemStats
	runtime.ReadMemStats(&baseline)
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sender := &a6UserLogBlockingSender{started: make(chan struct{})}
	svc := userlogsvc.NewService(userlogsvc.NewSQLiteRepository(db), sender, zap.NewNop())
	if err := svc.SetLogChat(ctx, 777); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	p := New(svc, 12345)
	p.SetEventBus(bus)
	scope := plugin.NewScope(ctx, "plugin:userlog")
	if err := p.InitScope(scope.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("plugin:userlog"); got != 2 {
		t.Fatalf("userlog event subscriptions=%d, want 2", got)
	}

	const burst = 1536
	for i := 0; i < burst; i++ {
		peerID := int64(2000 + i)
		msg := &core.MessageEnvelope{
			ChatID:     peerID,
			Chat:       core.Chat{ID: peerID, Type: "private"},
			Peer:       core.PeerRef{Kind: core.PeerKindUser, ID: peerID},
			SenderPeer: core.PeerRef{Kind: core.PeerKindUser, ID: peerID},
			Sender:     core.User{ID: peerID, FirstName: "Sender"},
			Text:       fmt.Sprintf("PM %d", i),
		}
		if i%3 == 0 {
			msg.ChatID = 100
			msg.Chat = core.Chat{ID: 100, Type: "group", Title: "Test Group"}
			msg.Peer = core.PeerRef{Kind: core.PeerKindChat, ID: 100}
			msg.Mentioned = true
		}
		if err := p.HandleMessageEvent(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-sender.started:
	case <-time.After(3 * time.Second):
		t.Fatal("lazy userlog worker never entered blocked send")
	}
	if got := len(p.queue); got > queueCapacity {
		t.Fatalf("observer pending queue=%d exceeds capacity=%d", got, queueCapacity)
	}
	if got := p.droppedCount.Load(); got == 0 {
		t.Fatal("full queue did not shed excess observer work")
	}
	if active := scope.ActiveGoroutines(); active != 1 {
		t.Fatalf("observer burst created %d physical workers, expected one lazy worker", active)
	}
	runtime.ReadMemStats(&peak)

	stopCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := p.ShutdownContext(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got := sender.calls.Load(); got != 1 {
		t.Fatalf("old observer generation sent %d RPCs after shutdown", got)
	}
	if got := bus.SubscriptionCount("plugin:userlog"); got != 0 {
		t.Fatalf("stale UserLog event subscriptions after shutdown: %d", got)
	}
	freshScope := plugin.NewScope(ctx, "plugin:userlog")
	if err := p.InitScope(freshScope.Context(), freshScope); err != nil {
		t.Fatalf("UserLog reload failed: %v", err)
	}
	if got := freshScope.ActiveGoroutines(); got != 0 {
		t.Fatalf("reload spawned %d idle workers", got)
	}
	if got := bus.SubscriptionCount("plugin:userlog"); got != 2 {
		t.Fatalf("reload subscriptions=%d, want 2", got)
	}
	if err := p.ShutdownContext(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := freshScope.Close(stopCtx); err != nil {
		t.Fatal(err)
	}
	if got := bus.SubscriptionCount("plugin:userlog"); got != 0 {
		t.Fatalf("reload leaked %d EventBus subscriptions", got)
	}
	runtime.GC()
	runtime.ReadMemStats(&settled)
	t.Logf("A7-C userlog: go=%s goroutines baseline=%d settled=%d heap baseline=%d peak=%d settled=%d bytes; burst=%d enqueued=%d dropped=%d",
		runtime.Version(), startGoroutines, runtime.NumGoroutine(),
		baseline.HeapAlloc, peak.HeapAlloc, settled.HeapAlloc,
		burst, p.enqueuedCount.Load(), p.droppedCount.Load())
}
