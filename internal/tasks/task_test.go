package tasks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTask_ExecuteSuccess(t *testing.T) {
	executed := false
	task := Task{
		ID:    "t-1",
		Owner: "plugin:test",
		Name:  "success",
		Run: func(ctx context.Context) error {
			executed = true
			return nil
		},
	}

	err := task.Execute(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if !executed {
		t.Errorf("expected task to be executed")
	}
	if task.State != StateCompleted {
		t.Errorf("expected StateCompleted, got %s", task.State)
	}
	if task.StartedAt.IsZero() || task.CompletedAt.IsZero() {
		t.Errorf("expected timestamps to be recorded")
	}
}

func TestTask_ExecuteTimeout(t *testing.T) {
	task := Task{
		ID:      "t-2",
		Owner:   "plugin:test",
		Name:    "timeout",
		Timeout: 20 * time.Millisecond,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	err := task.Execute(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
	if task.State != StateTimedOut {
		t.Errorf("expected StateTimedOut, got: %s", task.State)
	}
}

func TestTask_ExecuteRetry(t *testing.T) {
	var attempts atomic.Int32
	task := Task{
		ID:    "t-3",
		Owner: "plugin:test",
		Name:  "retryable",
		Retry: RetryPolicy{
			MaxAttempts: 3,
			Delay:       5 * time.Millisecond,
		},
		Run: func(ctx context.Context) error {
			count := attempts.Add(1)
			if count < 3 {
				return errors.New("transient error")
			}
			return nil
		},
	}

	err := task.Execute(context.Background())
	if err != nil {
		t.Fatalf("expected eventual success, got: %v", err)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
	if task.State != StateCompleted {
		t.Errorf("expected StateCompleted, got %s", task.State)
	}
}

func TestTask_ExecutePanicRecovery(t *testing.T) {
	task := Task{
		ID:    "t-4",
		Owner: "plugin:test",
		Name:  "panicking",
		Run: func(ctx context.Context) error {
			panic("unexpected boom")
		},
	}

	err := task.Execute(context.Background())
	if err == nil {
		t.Fatalf("expected error from recovered panic, got nil")
	}
	if task.State != StateFailed {
		t.Errorf("expected StateFailed, got %s", task.State)
	}
}
