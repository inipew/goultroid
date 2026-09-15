package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskCompletionIncludesSkippedExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := 0
	task := Task{
		ID: "cancel", Owner: "owner",
		Run: func(context.Context) error { t.Fatal("cancelled task executed"); return nil },
		OnComplete: func(err error) {
			called++
			if !errors.Is(err, context.Canceled) {
				t.Errorf("completion = %v", err)
			}
		},
	}
	if err := task.Execute(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if called != 1 || task.State != StateCancelled || !task.StartedAt.IsZero() {
		t.Fatalf("completion count=%d, task=%+v", called, task)
	}
}

func TestTaskCompletionIncludesPanicAndDeadline(t *testing.T) {
	for _, panicRun := range []bool{false, true} {
		t.Run(map[bool]string{true: "panic", false: "deadline"}[panicRun], func(t *testing.T) {
			var completed error
			called := 0
			task := Task{
				ID: "result", Owner: "owner", Timeout: time.Millisecond,
				Run: func(ctx context.Context) error {
					if panicRun {
						panic("boom")
					}
					<-ctx.Done()
					return ctx.Err()
				},
				OnComplete: func(err error) { called++; completed = err },
			}
			err := task.Execute(context.Background())
			if called != 1 || err == nil || completed != err {
				t.Fatalf("result=%v completion=%v calls=%d", err, completed, called)
			}
			if !panicRun && task.State != StateTimedOut {
				t.Fatalf("deadline state = %s", task.State)
			}
		})
	}
}
