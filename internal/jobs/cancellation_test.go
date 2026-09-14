package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

type blockingSubmitter struct {
	mu   sync.Mutex
	task tasks.Task
	ctx  context.Context
}

func (b *blockingSubmitter) Submit(ctx context.Context, _ string, task tasks.Task) error {
	b.mu.Lock()
	b.task = task
	b.ctx = ctx
	b.mu.Unlock()
	return nil
}

func TestCancelPropagatesToActiveJobAttempt(t *testing.T) {
	s := &blockingSubmitter{}
	m := NewManager(s)
	if err := m.Register(Job{
		ID: "cancel-me", Owner: "plugin:test",
		Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Trigger(context.Background(), "cancel-me"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	attemptCtx := s.ctx
	s.mu.Unlock()
	if attemptCtx == nil {
		t.Fatal("submitter did not receive attempt context")
	}
	if err := m.Cancel(context.Background(), "cancel-me"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attemptCtx.Done():
		if !errors.Is(attemptCtx.Err(), context.Canceled) {
			t.Fatalf("attempt error = %v", attemptCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("active attempt was not cancelled")
	}
}

func TestCancelByOwnerPropagatesToAllActiveAttempts(t *testing.T) {
	type submitted struct {
		ctx  context.Context
		task tasks.Task
	}
	var mu sync.Mutex
	var all []submitted
	s := taskSubmitterFunc(func(ctx context.Context, _ string, task tasks.Task) error {
		mu.Lock()
		all = append(all, submitted{ctx: ctx, task: task})
		mu.Unlock()
		return nil
	})
	m := NewManager(s)
	for _, id := range []string{"a", "b"} {
		if err := m.Register(Job{ID: id, Owner: "plugin:x", Run: func(context.Context) error { return nil }}); err != nil {
			t.Fatal(err)
		}
		if err := m.Trigger(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.CancelByOwner("plugin:x"); got != 2 {
		t.Fatalf("cancelled=%d want 2", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, sub := range all {
		select {
		case <-sub.ctx.Done():
		default:
			t.Fatalf("task %s context not cancelled", sub.task.ID)
		}
	}
}

type taskSubmitterFunc func(context.Context, string, tasks.Task) error

func (f taskSubmitterFunc) Submit(ctx context.Context, pool string, task tasks.Task) error {
	return f(ctx, pool, task)
}
