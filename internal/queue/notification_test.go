package queue

import (
	"context"
	"errors"
	"testing"
)

func TestTryPushAndSpaceNotification(t *testing.T) {
	q := New[int](1, PolicyBlock)
	if err := q.TryPush(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	changed := q.SpaceChanges()
	if err := q.TryPush(context.Background(), 2); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full push = %v", err)
	}
	if _, err := q.Pop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A pop between the failed push and select must not be lost.
	select {
	case <-changed:
	default:
		t.Fatal("space notification lost")
	}
	if err := q.TryPush(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	changed = q.SpaceChanges()
	q.Close()
	select {
	case <-changed:
	default:
		t.Fatal("close did not wake producer")
	}
	if err := q.TryPush(context.Background(), 3); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("closed push = %v", err)
	}
}

func TestTryPushRejectsCancelledContextWithAvailableSpace(t *testing.T) {
	q := New[int](1, PolicyBlock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.TryPush(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled push = %v", err)
	}
	if q.Depth() != 0 {
		t.Fatal("cancelled work was queued")
	}
}
