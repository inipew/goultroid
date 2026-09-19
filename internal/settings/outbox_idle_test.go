package settings

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestSettingsOutboxWorkerRetiresWhenHealthyAndRestartsOnCommit(t *testing.T) {
	repo, _ := setupTestSettingsRepo(t)
	reg := NewRegistry()
	if err := RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	svc := NewService(repo, reg, bus)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.lifecycleMu.Lock()
		running := svc.outboxRunning
		started := svc.started
		svc.lifecycleMu.Unlock()
		if !running {
			if !started {
				t.Fatal("service lifecycle stopped when only the outbox worker retired")
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	svc.lifecycleMu.Lock()
	if svc.outboxRunning {
		svc.lifecycleMu.Unlock()
		t.Fatal("healthy empty startup outbox retained a worker")
	}
	svc.lifecycleMu.Unlock()

	if err := svc.Set(context.Background(), ScopeGlobal, 0, "core", "prefix", "!", 1); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending, err := repo.ListPendingOutbox(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		svc.lifecycleMu.Lock()
		running := svc.outboxRunning
		svc.lifecycleMu.Unlock()
		if len(pending) == 0 && !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	pending, _ := repo.ListPendingOutbox(context.Background(), 10)
	svc.lifecycleMu.Lock()
	running := svc.outboxRunning
	svc.lifecycleMu.Unlock()
	t.Fatalf("outbox did not drain and retire: pending=%d running=%v", len(pending), running)
}
