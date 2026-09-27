package taskengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/admission"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestSetOwnerLimitsReportsApplied(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, MinConcurrency: 1, BacklogLimit: 8, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 8,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})

	started := make(chan struct{})
	if _, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "owner-limit-blocker", QuotaOwner: "system", Pool: "general",
		Handler: func(ctx context.Context) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocker did not start")
	}

	if err := engine.SetOwnerLimits("owner", admission.OwnerLimits{
		MaxWaiting: 1, MaxActive: 1, Weight: 3, MaxPayloadByte: 1 << 20,
	}); err != nil {
		t.Fatalf("SetOwnerLimits() error = %v", err)
	}

	if _, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "owner-limit-queued-1", QuotaOwner: "owner", Pool: "general",
		Handler: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("first owner task was not admitted: %v", err)
	}
	if _, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "owner-limit-queued-2", QuotaOwner: "owner", Pool: "general",
		Handler: func(context.Context) error { return nil },
	}); !errors.Is(err, tasks.ErrOwnerQueueFull) {
		t.Fatalf("second owner task error = %v, want ErrOwnerQueueFull", err)
	}

	releaseOnce.Do(func() { close(release) })
}

func TestSetOwnerLimitsContextCancellationDoesNotApply(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, MinConcurrency: 1, BacklogLimit: 8, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 8,
	})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = engine.Stop(ctx)
	})

	started := make(chan struct{})
	if _, err := engine.Submit(context.Background(), tasks.WorkSpec{
		ID: "owner-limit-cancel-blocker", QuotaOwner: "system", Pool: "general",
		Handler: func(ctx context.Context) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocker did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := engine.SetOwnerLimitsContext(ctx, "owner", admission.OwnerLimits{
		MaxWaiting: 1, MaxActive: 1, Weight: 9, MaxPayloadByte: 1 << 20,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SetOwnerLimitsContext() error = %v, want context.Canceled", err)
	}

	for i, id := range []tasks.TaskID{"owner-limit-cancel-1", "owner-limit-cancel-2"} {
		if _, err := engine.Submit(context.Background(), tasks.WorkSpec{
			ID: id, QuotaOwner: "owner", Pool: "general",
			Handler: func(context.Context) error { return nil },
		}); err != nil {
			t.Fatalf("owner task %d rejected after cancelled update: %v", i+1, err)
		}
	}

	releaseOnce.Do(func() { close(release) })
}

func TestOwnerLimitDecisionFenceRejectsCancelledRequest(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {Concurrency: 1, BacklogLimit: 8, PayloadBudget: 1 << 20},
		},
		ResultCapacity: 8,
	})

	decision := &controlCell{}
	if !decision.decide(controlDecisionCancelled) {
		t.Fatal("failed to pre-cancel owner-limit decision")
	}
	reply := make(chan engineReply, 1)
	engine.handleRequest(context.Background(), engineRequest{
		op: opSetOwnerLimits, owner: "owner",
		limits:          admission.OwnerLimits{MaxWaiting: 1, MaxActive: 1, Weight: 9, MaxPayloadByte: 1 << 20},
		controlDecision: decision,
		reply:           reply,
	})
	if rep := <-reply; rep.err == nil {
		t.Fatal("cancelled owner-limit request reported success")
	}

	first := tasks.WorkSpec{ID: "owner-fence-1", QuotaOwner: "owner", Pool: "general", Class: tasks.PriorityNormal}
	second := tasks.WorkSpec{ID: "owner-fence-2", QuotaOwner: "owner", Pool: "general", Class: tasks.PriorityNormal}
	engine.adm.Enqueue(&admission.QueueEntry{Spec: first})
	if err := engine.adm.CanAdmit(second, 0); err != nil {
		t.Fatalf("cancelled owner-limit request mutated admission state: %v", err)
	}
}
