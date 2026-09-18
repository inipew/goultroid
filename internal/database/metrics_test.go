package database

import (
	"errors"
	"testing"
	"time"
)

func TestDBMetrics_Noop(t *testing.T) {
	var m DBMetrics = NoopDBMetrics{}
	m.Observe("peer.find", 10*time.Millisecond, nil)
	m.Observe("peer.save", 20*time.Millisecond, errors.New("timeout"))
}

func TestDBMetrics_InMemory(t *testing.T) {
	m := NewInMemoryDBMetrics()
	m.Observe("peer.find", 10*time.Millisecond, nil)
	m.Observe("peer.find", 15*time.Millisecond, nil)
	m.Observe("peer.save", 25*time.Millisecond, errors.New("locked"))

	if m.Count("peer.find") != 2 {
		t.Fatalf("expected 2 peer.find, got %d", m.Count("peer.find"))
	}
	if m.Count("peer.save") != 1 {
		t.Fatalf("expected 1 peer.save, got %d", m.Count("peer.save"))
	}
	if m.Errors("peer.save") != 1 {
		t.Fatalf("expected 1 error on peer.save, got %d", m.Errors("peer.save"))
	}
	if m.TotalOperations() != 3 {
		t.Fatalf("expected 3 total operations, got %d", m.TotalOperations())
	}
}

func TestDB_Observe(t *testing.T) {
	db := &DB{}
	m := NewInMemoryDBMetrics()
	db.SetMetrics(m)

	db.Observe("settings.resolve", 5*time.Millisecond, nil)
	if m.Count("settings.resolve") != 1 {
		t.Fatalf("expected 1 settings.resolve call, got %d", m.Count("settings.resolve"))
	}
}

func TestDB_Metrics_ConcurrentAccess(t *testing.T) {
	db := &DB{}
	m := NewInMemoryDBMetrics()
	db.SetMetrics(m)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				db.SetMetrics(m)
				_ = db.Metrics()
			}
		}
	}()

	for i := 0; i < 50; i++ {
		db.Observe("concurrent.op", time.Millisecond, nil)
	}
	close(done)

	if m.Count("concurrent.op") != 50 {
		t.Fatalf("expected 50 ops, got %d", m.Count("concurrent.op"))
	}
}
