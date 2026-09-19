package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventBusStartsWithZeroDispatchWorkers(t *testing.T) {
	b := NewEventBus()
	b.workerIdleTimeout = 10 * time.Millisecond
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	stats := b.Stats()
	if stats.ActiveWorkers != 0 || stats.OrderedWorkers != 0 {
		t.Fatalf("idle start spawned workers: general=%d ordered=%d", stats.ActiveWorkers, stats.OrderedWorkers)
	}
}

func TestEventBusWorkersSpawnOnDemandAndRetire(t *testing.T) {
	b := NewEventBus()
	b.workerIdleTimeout = 10 * time.Millisecond
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var generalCalls atomic.Int32
	generalDone := make(chan struct{}, 1)
	b.Subscribe(EventTypeSettingChanged, func(Event) {
		generalCalls.Add(1)
		generalDone <- struct{}{}
	})
	b.Publish(&SettingChangedEvent{At: time.Now()})
	select {
	case <-generalDone:
	case <-time.After(time.Second):
		t.Fatal("general event was not delivered")
	}

	var orderedCalls atomic.Int32
	orderedDone := make(chan struct{}, 1)
	b.Subscribe(EventTypeMessageEdited, func(Event) {
		orderedCalls.Add(1)
		orderedDone <- struct{}{}
	})
	b.Publish(&MessageEditedEvent{At: time.Now(), ChatID: 42, MsgID: 1})
	select {
	case <-orderedDone:
	case <-time.After(time.Second):
		t.Fatal("ordered event was not delivered")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats := b.Stats()
		if stats.ActiveWorkers == 0 && stats.OrderedWorkers == 0 {
			if generalCalls.Load() != 1 || orderedCalls.Load() != 1 {
				t.Fatalf("delivery counts general=%d ordered=%d", generalCalls.Load(), orderedCalls.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	stats := b.Stats()
	t.Fatalf("event workers did not retire: general=%d ordered=%d", stats.ActiveWorkers, stats.OrderedWorkers)
}

func TestContextSubscriptionUsesCancellationWithoutWatcherWorker(t *testing.T) {
	b := NewEventBus()
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sub := b.SubscribeContext(ctx, "owner", EventTypeMessageCreated, func(Event) {})
	if sub == nil {
		t.Fatal("subscription was not created")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if b.SubscriptionCount("owner") == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("context cancellation did not close subscription")
}
