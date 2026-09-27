package taskengine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestCompletionDeliveryDrainWaitsForActiveCallback(t *testing.T) {
	d := newCompletionDelivery(1, 2)
	d.start()

	started := make(chan struct{})
	release := make(chan struct{})
	if !d.reserve() {
		t.Fatal("reserve callback credit")
	}
	if !d.enqueueReserved(func(tasks.TaskResult) {
		close(started)
		<-release
	}, tasks.TaskResult{}) {
		t.Fatal("enqueue callback")
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- d.drain(ctx) }()

	select {
	case err := <-drained:
		t.Fatalf("drain returned while callback was active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain after callback completion: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("event-driven drain was not signalled")
	}

	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop delivery: %v", err)
	}
}

func TestCompletionDeliveryDrainBroadcastsToConcurrentWaiters(t *testing.T) {
	d := newCompletionDelivery(2, 4)
	d.start()

	started := make(chan struct{})
	release := make(chan struct{})
	if !d.reserve() {
		t.Fatal("reserve callback credit")
	}
	if !d.enqueueReserved(func(tasks.TaskResult) {
		close(started)
		<-release
	}, tasks.TaskResult{}) {
		t.Fatal("enqueue callback")
	}
	<-started

	const waiters = 8
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, waiters)
	var ready sync.WaitGroup
	ready.Add(waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			ready.Done()
			results <- d.drain(ctx)
		}()
	}
	ready.Wait()
	close(release)

	for i := 0; i < waiters; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("drain waiter %d: %v", i, err)
			}
		case <-ctx.Done():
			t.Fatalf("drain waiter %d missed broadcast: %v", i, ctx.Err())
		}
	}
	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop delivery: %v", err)
	}
}

func TestCompletionDeliveryDrainDeadlineWins(t *testing.T) {
	d := newCompletionDelivery(1, 2)
	d.start()

	started := make(chan struct{})
	release := make(chan struct{})
	if !d.reserve() {
		t.Fatal("reserve callback credit")
	}
	if !d.enqueueReserved(func(tasks.TaskResult) {
		close(started)
		<-release
	}, tasks.TaskResult{}) {
		t.Fatal("enqueue callback")
	}
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := d.drain(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain error=%v, want deadline exceeded", err)
	}

	close(release)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := d.drain(stopCtx); err != nil {
		t.Fatalf("drain after release: %v", err)
	}
	if err := d.stop(stopCtx); err != nil {
		t.Fatalf("stop delivery: %v", err)
	}
}
