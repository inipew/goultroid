package database

import (
	"errors"
	"sync"
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
	if got := m.TotalDuration("peer.find"); got != 25*time.Millisecond {
		t.Fatalf("expected peer.find total duration 25ms, got %s", got)
	}
	if got := m.TotalDuration("peer.save"); got != 25*time.Millisecond {
		t.Fatalf("expected peer.save total duration 25ms, got %s", got)
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

func TestDBMetrics_ConcurrentObserveExactCounts(t *testing.T) {
	m := NewInMemoryDBMetrics()
	const goroutines = 16
	const perGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for worker := 0; worker < goroutines; worker++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				var err error
				if i%10 == 0 {
					err = errors.New("synthetic")
				}
				m.Observe("peer.find", time.Microsecond, err)
			}
		}(worker)
	}
	wg.Wait()

	want := int64(goroutines * perGoroutine)
	if got := m.Count("peer.find"); got != want {
		t.Fatalf("count=%d, want %d", got, want)
	}
	if got := m.TotalOperations(); got != want {
		t.Fatalf("total operations=%d, want %d", got, want)
	}
	if got := m.Errors("peer.find"); got != want/10 {
		t.Fatalf("errors=%d, want %d", got, want/10)
	}
	if got := m.TotalDuration("peer.find"); got != time.Duration(want)*time.Microsecond {
		t.Fatalf("duration=%s, want %s", got, time.Duration(want)*time.Microsecond)
	}
}

func TestDBMetrics_ConcurrentCollectorReplacementAndObserve(t *testing.T) {
	db := &DB{}
	primary := NewInMemoryDBMetrics()
	secondary := NewInMemoryDBMetrics()
	db.SetMetrics(primary)

	const observations = 5000
	done := make(chan struct{})
	var swapWG sync.WaitGroup
	swapWG.Add(1)
	go func() {
		defer swapWG.Done()
		for {
			select {
			case <-done:
				return
			default:
				db.SetMetrics(secondary)
				db.SetMetrics(NoopDBMetrics{})
				db.SetMetrics(primary)
				_ = db.Metrics()
			}
		}
	}()

	for i := 0; i < observations; i++ {
		db.Observe("concurrent.swap", time.Microsecond, nil)
	}
	close(done)
	swapWG.Wait()

	got := primary.Count("concurrent.swap") + secondary.Count("concurrent.swap")
	if got > observations {
		t.Fatalf("collectors observed %d operations, maximum is %d", got, observations)
	}

	// Restore a deterministic collector and prove Observe still works after
	// replacing the concrete DBMetrics implementation concurrently.
	db.SetMetrics(primary)
	before := primary.Count("concurrent.swap")
	db.Observe("concurrent.swap", time.Microsecond, nil)
	if got := primary.Count("concurrent.swap"); got != before+1 {
		t.Fatalf("post-swap observe count=%d, want %d", got, before+1)
	}
}

func BenchmarkDBMetricsObserveWarmLabel(b *testing.B) {
	m := NewInMemoryDBMetrics()
	m.Observe("peer.find", time.Microsecond, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Observe("peer.find", time.Microsecond, nil)
	}
}

func BenchmarkDBObserveWarmMetrics(b *testing.B) {
	db := &DB{}
	db.SetMetrics(NewInMemoryDBMetrics())
	db.Observe("peer.find", time.Microsecond, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db.Observe("peer.find", time.Microsecond, nil)
	}
}
