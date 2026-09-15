package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistencePump_EnqueueAndProcess(t *testing.T) {
	pump := NewPersistencePump(2, 10)
	if err := pump.Start(context.Background()); err != nil {
		t.Fatalf("start pump failed: %v", err)
	}
	defer pump.Stop(context.Background())

	var executed atomic.Bool
	resCh, err := pump.Enqueue(context.Background(), func(ctx context.Context) error {
		executed.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case err := <-resCh:
		if err != nil {
			t.Fatalf("op failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for op completion")
	}

	if !executed.Load() {
		t.Errorf("expected op to be executed")
	}
}

func TestPersistencePump_QueueSaturation(t *testing.T) {
	pump := NewPersistencePump(1, 1) // 1 worker, 1 queue buffer
	_ = pump.Start(context.Background())
	defer pump.Stop(context.Background())
	blocker := make(chan struct{})
	defer close(blocker)

	started := make(chan struct{})
	// Enqueue op that occupies the single worker
	_, err := pump.Enqueue(context.Background(), func(ctx context.Context) error {
		close(started)
		<-blocker
		return nil
	})
	if err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Wait until worker has dequeued and started the task
	<-started

	// Enqueue op that fills the buffer (1 slot)
	_, err = pump.Enqueue(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	// Third enqueue must be rejected with ErrPumpQueueFull
	_, err = pump.Enqueue(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if !errors.Is(err, ErrPumpQueueFull) {
		t.Errorf("expected ErrPumpQueueFull, got: %v", err)
	}
}
