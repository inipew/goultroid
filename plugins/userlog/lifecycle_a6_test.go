package userlog

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	userlogsvc "github.com/inipew/goultroid/internal/services/userlog"
	"go.uber.org/zap"
)

type a6UserLogBlockingSender struct {
	calls   atomic.Int64
	started chan struct{}
}

func (s *a6UserLogBlockingSender) SendMessage(ctx context.Context, _ tg.InputPeerClass, _ string) (*tg.Message, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestA6UserLogFullQueueShutdownCancelsDeliveryWithoutDrainingRPCs(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sender := &a6UserLogBlockingSender{started: make(chan struct{})}
	svc := userlogsvc.NewService(userlogsvc.NewSQLiteRepository(db), sender, zap.NewNop())
	if err := svc.SetLogChat(context.Background(), 777); err != nil {
		t.Fatal(err)
	}
	p := New(svc, 12345)
	scope := plugin.NewScope(context.Background(), "plugin:userlog")
	if err := p.InitScope(scope.Context(), scope); err != nil {
		t.Fatal(err)
	}
	msg := &core.MessageEnvelope{
		ChatID:     999,
		Chat:       core.Chat{ID: 999, Type: "private"},
		Peer:       core.PeerRef{Kind: core.PeerKindUser, ID: 999},
		SenderPeer: core.PeerRef{Kind: core.PeerKindUser, ID: 999},
		Sender:     core.User{ID: 999, FirstName: "Sender"},
		Text:       "message content",
	}
	if err := p.HandleMessageEvent(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sender.started:
	case <-time.After(2 * time.Second):
		t.Fatal("lazy userlog worker failed to start")
	}
	for i := 0; i < queueCapacity*3; i++ {
		if err := p.HandleMessageEvent(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(p.queue); got > queueCapacity {
		t.Fatalf("unbounded UserLog pending queue: %d", got)
	}
	if p.droppedCount.Load() == 0 {
		t.Fatal("full observer queue did not drop excess low-priority work")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.ShutdownContext(ctx); err != nil {
		t.Fatalf("shutdown waited for full queue instead of canceling: %v", err)
	}
	if got := sender.calls.Load(); got != 1 {
		t.Fatalf("queued sends leaked beyond plugin shutdown: %d", got)
	}
	if err := scope.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := scope.ActiveGoroutines(); got != 0 {
		t.Fatalf("UserLog goroutines not settled after shutdown: %d", got)
	}
}
