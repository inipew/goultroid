package core

import (
	"context"
	"testing"
	"time"
)

func TestEventBusWorkerRetirementHandoffCannotStrandQueuedWork(t *testing.T) {
	b := NewEventBus()
	b.workerIdleTimeout = 2 * time.Millisecond
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	done := make(chan struct{}, 128)
	b.Subscribe(EventTypeSettingChanged, func(Event) { done <- struct{}{} })

	for i := 0; i < 100; i++ {
		b.Publish(&SettingChangedEvent{At: time.Now()})
		time.Sleep(2 * time.Millisecond)
	}

	deadline := time.After(2 * time.Second)
	for received := 0; received < 100; {
		select {
		case <-done:
			received++
		case <-deadline:
			t.Fatalf("event stranded across worker retirement, delivered=%d/100", received)
		}
	}
}
