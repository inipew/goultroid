package core

import (
	"testing"
	"time"
)

func TestEventBusDefaultIdleWindow(t *testing.T) {
	if defaultEventWorkerIdle != 10*time.Second {
		t.Fatalf("default EventBus idle timeout = %s, want 10s", defaultEventWorkerIdle)
	}
	bus := NewEventBus()
	if bus.workerIdleTimeout != defaultEventWorkerIdle {
		t.Fatalf("EventBus idle timeout = %s, want %s", bus.workerIdleTimeout, defaultEventWorkerIdle)
	}
}
