package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueue_FIFO(t *testing.T) {
	q := New[string](5, PolicyReject)

	ctx := context.Background()
	_ = q.Push(ctx, "a")
	_ = q.Push(ctx, "b")
	_ = q.Push(ctx, "c")

	if q.Depth() != 3 {
		t.Fatalf("expected depth 3, got %d", q.Depth())
	}

	val, err := q.Pop(ctx)
	if err != nil || val != "a" {
		t.Errorf("expected 'a', got %s, err: %v", val, err)
	}

	val, err = q.Pop(ctx)
	if err != nil || val != "b" {
		t.Errorf("expected 'b', got %s, err: %v", val, err)
	}

	val, err = q.Pop(ctx)
	if err != nil || val != "c" {
		t.Errorf("expected 'c', got %s, err: %v", val, err)
	}

	if q.Depth() != 0 {
		t.Errorf("expected depth 0, got %d", q.Depth())
	}
}

func TestQueue_PolicyReject(t *testing.T) {
	q := New[int](2, PolicyReject)
	ctx := context.Background()

	_ = q.Push(ctx, 1)
	_ = q.Push(ctx, 2)

	err := q.Push(ctx, 3)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}

	stats := q.Stats()
	if stats.Rejected != 1 {
		t.Errorf("expected 1 rejected item, got %d", stats.Rejected)
	}
}

func TestQueue_PolicyDrop(t *testing.T) {
	q := New[int](2, PolicyDrop)
	ctx := context.Background()

	_ = q.Push(ctx, 1)
	_ = q.Push(ctx, 2)

	err := q.Push(ctx, 3)
	if err != nil {
		t.Fatalf("expected nil error on dropped item, got %v", err)
	}

	stats := q.Stats()
	if stats.Dropped != 1 {
		t.Errorf("expected 1 dropped, got %d", stats.Dropped)
	}

	// Should still contain 1 and 2
	v1, _ := q.Pop(ctx)
	v2, _ := q.Pop(ctx)
	if v1 != 1 || v2 != 2 {
		t.Errorf("unexpected queue items: %d, %d", v1, v2)
	}
}

func TestQueue_PolicyDropOldest(t *testing.T) {
	q := New[int](2, PolicyDropOldest)
	ctx := context.Background()

	_ = q.Push(ctx, 1)
	_ = q.Push(ctx, 2)
	_ = q.Push(ctx, 3) // evicts 1, keeps 2, 3

	v1, _ := q.Pop(ctx)
	v2, _ := q.Pop(ctx)
	if v1 != 2 || v2 != 3 {
		t.Errorf("expected [2, 3] after dropping oldest, got [%d, %d]", v1, v2)
	}
}

func TestQueue_BlockAndCancel(t *testing.T) {
	q := New[int](1, PolicyBlock)
	ctx := context.Background()
	_ = q.Push(ctx, 10)

	pushCtx, pushCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer pushCancel()

	err := q.Push(pushCtx, 20)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded on blocked push, got: %v", err)
	}
}

func TestQueue_Close(t *testing.T) {
	q := New[int](5, PolicyBlock)

	var wg sync.WaitGroup
	wg.Add(1)

	var popErr error
	go func() {
		defer wg.Done()
		_, popErr = q.Pop(context.Background())
	}()

	time.Sleep(20 * time.Millisecond)
	q.Close()
	wg.Wait()

	if !errors.Is(popErr, ErrQueueClosed) {
		t.Errorf("expected ErrQueueClosed upon Close, got %v", popErr)
	}
}
