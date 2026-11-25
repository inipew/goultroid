package taskengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestU6_S03A_PoolBacklogSaturationRejection(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {
				Concurrency:    1,
				MinConcurrency: 1,
				BacklogLimit:   1,
				PayloadBudget:  1 << 20,
			},
		},
		ResultCapacity: 16,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	var releaseOnce sync.Once
	releaseCh := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = engine.Stop(stopCtx)
	})

	startedCh := make(chan struct{})
	// 1. Submit task 1 and hold its handler on barrier until worker is active.
	t1, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s03a-active",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Handler: func(runCtx context.Context) error {
			close(startedCh)
			select {
			case <-releaseCh:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("submit task 1: %v", err)
	}
	select {
	case <-startedCh:
	case <-ctx.Done():
		t.Fatalf("task 1 did not start: %v", ctx.Err())
	}

	// 2. Submit task 2 to fill BacklogLimit=1.
	t2, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s03a-backlog",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Handler:    func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("submit task 2 (backlog): %v", err)
	}

	// 3. Submit task 3 -> must be rejected synchronously with AdmissionError.
	_, err = engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s03a-overflow",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Handler:    func(context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("expected task 3 submission to be rejected when pool backlog is full")
	}
	var admErr *tasks.AdmissionError
	if !errors.As(err, &admErr) {
		t.Fatalf("expected *tasks.AdmissionError, got %T: %v", err, err)
	}
	if engine.admissionRejections.Load() == 0 {
		t.Fatal("expected admissionRejections counter > 0")
	}

	// 4. Release barrier and confirm admitted tasks 1 & 2 complete cleanly.
	releaseOnce.Do(func() { close(releaseCh) })
	if res1, werr := t1.Wait(ctx); werr != nil || res1.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("task 1 failed: res=%+v err=%v", res1, werr)
	}
	if res2, werr := t2.Wait(ctx); werr != nil || res2.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("task 2 failed: res=%+v err=%v", res2, werr)
	}
}

func TestU6_S03B_RetainedByteBudgetRejection(t *testing.T) {
	engine := NewEngine(Config{
		Pools: map[tasks.PoolID]PoolEngineConfig{
			"general": {
				Concurrency:    1,
				MinConcurrency: 1,
				BacklogLimit:   8,
				PayloadBudget:  1 << 20,
			},
		},
		ResultCapacity:   16,
		MaxRetainedBytes: 1024,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	var releaseOnce sync.Once
	releaseCh := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCh) })
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = engine.Stop(stopCtx)
	})

	startedCh := make(chan struct{})
	t1, err := engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s03b-fit",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Input:      []byte("small"),
		Handler: func(runCtx context.Context) error {
			close(startedCh)
			select {
			case <-releaseCh:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		},
	})
	if err != nil {
		t.Fatalf("submit task 1: %v", err)
	}
	select {
	case <-startedCh:
	case <-ctx.Done():
		t.Fatalf("task 1 did not start: %v", ctx.Err())
	}

	// Backlog still has plenty of slots (8), but payload exceeds remaining MaxRetainedBytes (1024).
	_, err = engine.Submit(ctx, tasks.WorkSpec{
		ID:         "s03b-too-large",
		QuotaOwner: "owner-1",
		Pool:       "general",
		Input:      make([]byte, 1024),
		Handler:    func(context.Context) error { return nil },
	})
	if !errors.Is(err, tasks.ErrRetainedBudget) {
		t.Fatalf("expected ErrRetainedBudget, got: %v", err)
	}

	releaseOnce.Do(func() { close(releaseCh) })
	if res1, werr := t1.Wait(ctx); werr != nil || res1.Outcome != tasks.OutcomeCompleted {
		t.Fatalf("task 1 failed: res=%+v err=%v", res1, werr)
	}
}
