package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskManager_RegisterAndQuota(t *testing.T) {
	mgr := NewManager()
	mgr.SetOwnerQuota("plugin-a", Quota{MaxConcurrent: 1, MaxQueued: 2})

	task1 := Task{
		ID:    "task-1",
		Owner: "plugin-a",
		Run:   func(ctx context.Context) error { return nil },
	}
	task2 := Task{
		ID:    "task-2",
		Owner: "plugin-a",
		Run:   func(ctx context.Context) error { return nil },
	}
	task3 := Task{
		ID:    "task-3",
		Owner: "plugin-a",
		Run:   func(ctx context.Context) error { return nil },
	}

	ctx := context.Background()
	_, _, err := mgr.Register(ctx, task1)
	if err != nil {
		t.Fatalf("unexpected error registering task 1: %v", err)
	}

	_, _, err = mgr.Register(ctx, task2)
	if err != nil {
		t.Fatalf("unexpected error registering task 2: %v", err)
	}

	// 3rd task should exceed MaxQueued (2)
	_, _, err = mgr.Register(ctx, task3)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}

	// Duplicate registration of task1
	_, _, err = mgr.Register(ctx, task1)
	if !errors.Is(err, ErrTaskExists) {
		t.Fatalf("expected ErrTaskExists, got %v", err)
	}
}

func TestTaskManager_StartAndConcurrencyQuota(t *testing.T) {
	mgr := NewManager()
	mgr.SetOwnerQuota("plugin-b", Quota{MaxConcurrent: 1, MaxQueued: 5})

	task1 := Task{
		ID:    "b-1",
		Owner: "plugin-b",
		Run:   func(ctx context.Context) error { return nil },
	}
	task2 := Task{
		ID:    "b-2",
		Owner: "plugin-b",
		Run:   func(ctx context.Context) error { return nil },
	}

	ctx := context.Background()
	if _, _, err := mgr.Register(ctx, task1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Register(ctx, task2); err != nil {
		t.Fatal(err)
	}

	// Start task1
	if _, err := mgr.TryStart("b-1"); err != nil {
		t.Fatalf("unexpected error starting task 1: %v", err)
	}

	// Starting task2 should fail due to MaxConcurrent=1
	if _, err := mgr.TryStart("b-2"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded for concurrent task, got %v", err)
	}

	// Finish task1
	mgr.Finish("b-1", StateCompleted, nil)

	// Now task2 should be startable
	if _, err := mgr.TryStart("b-2"); err != nil {
		t.Fatalf("expected b-2 to start after b-1 finished, got: %v", err)
	}

	mgr.Finish("b-2", StateCompleted, nil)

	stats := mgr.Stats()
	if stats.TotalCompleted != 2 {
		t.Fatalf("expected 2 completed tasks, got %d", stats.TotalCompleted)
	}
	if stats.Owners["plugin-b"].Running != 0 || stats.Owners["plugin-b"].Queued != 0 {
		t.Fatalf("expected 0 running and queued for plugin-b, got %+v", stats.Owners["plugin-b"])
	}
}

func TestTaskManager_WaitStartWaitsForConcurrencySlot(t *testing.T) {
	mgr := NewManager()
	mgr.SetOwnerQuota("plugin-wait", Quota{MaxConcurrent: 1, MaxQueued: 2})
	ctx := context.Background()
	for _, id := range []string{"wait-1", "wait-2"} {
		if _, _, err := mgr.Register(ctx, Task{ID: id, Owner: "plugin-wait", Run: func(context.Context) error { return nil }}); err != nil {
			t.Fatalf("Register(%s) error = %v", id, err)
		}
	}
	if _, err := mgr.TryStart("wait-1"); err != nil {
		t.Fatalf("Start(wait-1) error = %v", err)
	}

	result := make(chan error, 1)
	go func() {
		_, err := mgr.WaitStart(ctx, "wait-2")
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("WaitStart returned while slot was full: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	mgr.Finish("wait-1", StateCompleted, nil)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("WaitStart error after slot release = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitStart did not acquire released slot")
	}
	mgr.Finish("wait-2", StateCompleted, nil)
}

func TestTaskManager_CancelAndCancelByOwner(t *testing.T) {
	mgr := NewManager()

	task1 := Task{ID: "c-1", Owner: "owner-c", Run: func(ctx context.Context) error { return nil }}
	task2 := Task{ID: "c-2", Owner: "owner-c", Run: func(ctx context.Context) error { return nil }}
	task3 := Task{ID: "d-1", Owner: "owner-d", Run: func(ctx context.Context) error { return nil }}

	ctx := context.Background()
	ctx1, _, err := mgr.Register(ctx, task1)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, _, err := mgr.Register(ctx, task2)
	if err != nil {
		t.Fatal(err)
	}
	ctx3, _, err := mgr.Register(ctx, task3)
	if err != nil {
		t.Fatal(err)
	}

	// Cancel individual task
	if !mgr.Cancel("d-1") {
		t.Fatalf("expected cancel d-1 to return true")
	}
	select {
	case <-ctx3.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected ctx3 to be cancelled")
	}

	// Cancel by owner
	cancelled := mgr.CancelByOwner("owner-c")
	if cancelled != 2 {
		t.Fatalf("expected 2 cancelled tasks for owner-c, got %d", cancelled)
	}

	select {
	case <-ctx1.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected ctx1 to be cancelled")
	}
	select {
	case <-ctx2.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("expected ctx2 to be cancelled")
	}
}
