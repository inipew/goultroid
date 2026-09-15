package telegram

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/workers"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestDispatcherStopClosesAdmissionBeforeWait(t *testing.T) {
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.mu.Lock()
	d.inFlight.Add(1)
	d.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- d.Stop(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for d.acceptingUpdates.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if d.acceptingUpdates.Load() {
		t.Fatal("dispatcher did not quiesce ingress")
	}

	if err := d.dispatch(context.Background(), tg.Entities{}, &tg.Message{
		ID:      1,
		Message: ".noop",
		PeerID:  &tg.PeerChat{ChatID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	d.inFlight.Done()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish")
	}
}

func TestObserverCancelledBeforeStartDoesNotLeakInFlight(t *testing.T) {
	manager := workers.NewManager()
	manager.SetTasksManager(tasks.NewManager())
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		if err := manager.TrySubmit(context.Background(), workers.PoolGeneral, tasks.Task{ID: fmt.Sprintf("block-%d", i), Owner: fmt.Sprintf("owner-%d", i), Run: func(context.Context) error { started <- struct{}{}; <-release; return nil }}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("blocker did not start")
		}
	}
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	d.SetWorkers(manager)
	ctx, cancel := context.WithCancel(context.Background())
	d.dispatchAsyncHandlers(ctx, []MessageHandler{func(context.Context, tg.Entities, *tg.Message, bool, string) error {
		t.Error("cancelled observer executed")
		return nil
	}}, tg.Entities{}, &tg.Message{ID: 123, PeerID: &tg.PeerChat{ChatID: 1}}, false, "")
	cancel()
	close(release)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("observer leaked in-flight counter: %v", err)
	}
}
