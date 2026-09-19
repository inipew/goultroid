package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

func TestCompletionDeliveryWorkersAreLazyAndRetire(t *testing.T) {
	d := newCompletionDelivery(2, 4)
	d.idleTimeout = 10 * time.Millisecond
	d.start()
	if got := d.remaining.Load(); got != 0 {
		t.Fatalf("delivery started %d idle workers, want 0", got)
	}
	if !d.reserve() {
		t.Fatal("reserve callback credit")
	}
	called := make(chan struct{})
	if !d.enqueueReserved(func(tasks.TaskResult) { close(called) }, tasks.TaskResult{}) {
		t.Fatal("enqueue callback")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("callback did not execute")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && d.remaining.Load() != 0 {
		time.Sleep(time.Millisecond)
	}
	if got := d.remaining.Load(); got != 0 {
		t.Fatalf("delivery retained %d workers after idle timeout", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop delivery: %v", err)
	}
}

func TestCompletionDeliveryScalesForActiveAndQueuedDemand(t *testing.T) {
	d := newCompletionDelivery(2, 4)
	d.idleTimeout = time.Second
	d.start()

	block := make(chan struct{})
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		if !d.reserve() {
			t.Fatalf("reserve %d", i)
		}
		if !d.enqueueReserved(func(tasks.TaskResult) {
			started <- struct{}{}
			<-block
		}, tasks.TaskResult{}) {
			t.Fatalf("enqueue %d", i)
		}
	}

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("delivery failed to scale to configured concurrency")
		}
	}
	close(block)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.drain(ctx); err != nil {
		t.Fatalf("drain delivery: %v", err)
	}
	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop delivery: %v", err)
	}
}

func TestDurabilityLaneWorkersAreLazyAndRetire(t *testing.T) {
	d := newDurabilityLane(2, 4)
	d.idleTimeout = 10 * time.Millisecond
	d.start()
	if got := d.remaining.Load(); got != 0 {
		t.Fatalf("durability lane started %d idle workers, want 0", got)
	}
	called := make(chan struct{})
	if !d.enqueue(func() { close(called) }) {
		t.Fatal("enqueue durability work")
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("durability work did not execute")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && d.remaining.Load() != 0 {
		time.Sleep(time.Millisecond)
	}
	if got := d.remaining.Load(); got != 0 {
		t.Fatalf("durability lane retained %d workers after idle timeout", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop durability lane: %v", err)
	}
}

func TestDurabilityLaneScalesForActiveAndQueuedDemand(t *testing.T) {
	d := newDurabilityLane(2, 4)
	d.idleTimeout = time.Second
	d.start()

	block := make(chan struct{})
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		if !d.enqueue(func() {
			started <- struct{}{}
			<-block
		}) {
			t.Fatalf("enqueue durability %d", i)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("durability lane failed to scale to configured concurrency")
		}
	}
	close(block)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (d.pending.Load() != 0 || d.active.Load() != 0) {
		time.Sleep(time.Millisecond)
	}
	if d.pending.Load() != 0 || d.active.Load() != 0 {
		t.Fatal("durability lane did not settle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.stop(ctx); err != nil {
		t.Fatalf("stop durability lane: %v", err)
	}
}
