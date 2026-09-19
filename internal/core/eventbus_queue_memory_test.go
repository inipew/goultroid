package core

import (
	"context"
	"testing"
	"time"
	"unsafe"
)

func TestEventBusQueueBackingStoreIsPointerSized(t *testing.T) {
	b := NewEventBus()
	pointerSize := unsafe.Sizeof((*eventJob)(nil))
	valueSize := unsafe.Sizeof(eventJob{})
	if valueSize <= pointerSize {
		t.Fatalf("eventJob unexpectedly pointer-sized: value=%d pointer=%d", valueSize, pointerSize)
	}

	totalSlots := cap(b.queue) + cap(b.queueCritical) + cap(b.queueHigh) + cap(b.queueLow)
	for i := range b.orderedQueues {
		totalSlots += cap(b.orderedQueues[i])
	}
	pointerBacking := uintptr(totalSlots) * pointerSize
	legacyBacking := uintptr(totalSlots) * valueSize
	if pointerBacking >= legacyBacking {
		t.Fatalf("pointer-backed queues did not reduce backing store: pointer=%d legacy=%d", pointerBacking, legacyBacking)
	}
}

func TestEventBusJobPoolClearsReferences(t *testing.T) {
	b := NewEventBus()
	job := b.acquireJob(eventJob{event: &MessageCreatedEvent{At: time.Now()}})
	if job.event == nil {
		t.Fatal("test envelope not populated")
	}
	b.releaseJob(job)
	reused := b.acquireJob(eventJob{})
	if reused.event != nil || reused.subscriber.handler != nil || reused.subscriber.contextHandler != nil {
		t.Fatalf("pooled event job retained references: %+v", reused)
	}
	b.releaseJob(reused)
}

func TestEventBusPointerQueuesStillDeliver(t *testing.T) {
	b := NewEventBus()
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	done := make(chan struct{}, 1)
	b.Subscribe(EventTypeMessageCreated, func(Event) { done <- struct{}{} })
	b.Publish(&MessageCreatedEvent{At: time.Now()})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pointer-backed event queue did not deliver")
	}
}
