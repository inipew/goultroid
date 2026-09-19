package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventBusCloseDrainsOrderedPointerQueue(t *testing.T) {
	b := NewEventBus()
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	b.Subscribe(EventTypeMessageEdited, func(Event) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
	})

	// Same ordering key forces both jobs through one ordered partition. The first
	// blocks the worker while the second remains queued for the shutdown drain.
	b.Publish(&MessageEditedEvent{At: time.Now(), ChatID: 42, MsgID: 1})
	b.Publish(&MessageEditedEvent{At: time.Now(), ChatID: 42, MsgID: 2})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("ordered worker did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	close(release)

	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("EventBus Close did not drain ordered queue")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("ordered calls=%d, want 2 after shutdown drain", got)
	}
}
