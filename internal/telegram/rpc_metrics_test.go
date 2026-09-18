package telegram

import (
	"testing"
	"time"
)

func TestNoopRPCMetrics(t *testing.T) {
	var m RPCMetrics = NoopRPCMetrics{}
	m.ObserveRequest("messages.sendMessage", RPCUnknown, 1, 10*time.Millisecond)
	m.ObserveWait("global", 5*time.Millisecond)
	m.ObserveFloodWait("messages.sendMessage", 2*time.Second, false)
}

func TestInMemoryRPCMetrics_Snapshot(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	m.ObserveRequest("messages.sendMessage", RPCUnknown, 1, 10*time.Millisecond)
	m.ObserveRequest("contacts.resolveUsername", RPCTransient, 2, 25*time.Millisecond)
	m.ObserveWait("peer", 15*time.Millisecond)
	m.ObserveFloodWait("messages.sendMessage", 3*time.Second, true)

	snap := m.Snapshot()
	if snap.TotalRequests != 2 {
		t.Fatalf("expected 2 requests, got %d", snap.TotalRequests)
	}
	if snap.RequestsByClass[RPCTransient] != 1 {
		t.Fatalf("expected 1 transient, got %d", snap.RequestsByClass[RPCTransient])
	}
	if snap.TotalWaitTime != 15*time.Millisecond {
		t.Fatalf("expected 15ms wait, got %v", snap.TotalWaitTime)
	}
	if snap.FloodWaitCount != 1 || snap.FloodWaitTotal != 3*time.Second {
		t.Fatalf("expected 1 floodwait of 3s, got %d / %v", snap.FloodWaitCount, snap.FloodWaitTotal)
	}

	// Ensure modifying snapshot doesn't corrupt underlying state
	snap.RequestsByClass[RPCFloodWait] = 999
	snap2 := m.Snapshot()
	if snap2.RequestsByClass[RPCFloodWait] != 0 {
		t.Fatalf("snapshot mutation leaked to internal state")
	}
}
