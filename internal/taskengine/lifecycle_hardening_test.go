package taskengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/runtime"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestParentCancellationSettlesDrainAndGracefulStop(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1024},
		},
		ResultCapacity: 4,
		InboxCapacity:  8,
	})
	if err := e.Start(parent); err != nil {
		t.Fatal(err)
	}

	e.mu.Lock()
	oldInbox := e.inbox
	e.mu.Unlock()

	cancelParent()

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err := e.Drain(drainCtx); err != nil {
		t.Fatalf("Drain after parent cancellation: %v", err)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := e.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after parent cancellation: %v", err)
	}

	e.mu.Lock()
	currentInbox := e.inbox
	e.mu.Unlock()
	if currentInbox != nil {
		t.Fatal("coordinator inbox remains published after shutdown")
	}
	if got := len(oldInbox); got != 0 {
		t.Fatalf("stale coordinator inbox retained %d request(s)", got)
	}
}

func TestPostStopControlAPIsReturnPromptly(t *testing.T) {
	e := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"p": {Concurrency: 1, BacklogLimit: 4, PayloadBudget: 1024},
		},
		ResultCapacity: 4,
		InboxCapacity:  8,
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	e.mu.Lock()
	oldInbox := e.inbox
	e.mu.Unlock()

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	if err := e.Stop(stopCtx); err != nil {
		cancelStop()
		t.Fatalf("Stop: %v", err)
	}
	cancelStop()

	done := make(chan error, 1)
	go func() {
		for i := 0; i < 32; i++ {
			if _, err := e.Stats(context.Background()); !errors.Is(err, tasks.ErrEngineQuiescing) {
				done <- fmt.Errorf("Stats post-stop error=%v, want engine quiescing", err)
				return
			}
			health := e.Health(context.Background())
			if health.Status == runtime.HealthHealthy {
				done <- fmt.Errorf("Health post-stop status=%v, want degraded", health.Status)
				return
			}
			if _, err := e.Cancel(tasks.TaskID(fmt.Sprintf("cancel-%d", i)), tasks.CauseUserCancel); !errors.Is(err, tasks.ErrEngineQuiescing) {
				done <- fmt.Errorf("Cancel post-stop error=%v, want engine quiescing", err)
				return
			}
			if _, err := e.Submit(context.Background(), tasks.WorkSpec{
				ID:         tasks.TaskID(fmt.Sprintf("submit-%d", i)),
				QuotaOwner: "owner",
				Pool:       "p",
				Handler:    func(context.Context) error { return nil },
			}); !errors.Is(err, tasks.ErrEngineQuiescing) {
				done <- fmt.Errorf("Submit post-stop error=%v, want engine quiescing", err)
				return
			}
			e.SetOwnerLimits("owner", admission.OwnerLimits{MaxActive: 1, MaxWaiting: 1, Weight: 1})
			if err := e.Quiesce(context.Background()); err != nil {
				done <- fmt.Errorf("Quiesce post-stop: %w", err)
				return
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("post-stop control APIs did not return promptly")
	}

	e.mu.Lock()
	currentInbox := e.inbox
	e.mu.Unlock()
	if currentInbox != nil {
		t.Fatal("coordinator inbox was republished after post-stop calls")
	}
	if got := len(oldInbox); got != 0 {
		t.Fatalf("post-stop calls leaked %d request(s) into stale coordinator inbox", got)
	}
}
