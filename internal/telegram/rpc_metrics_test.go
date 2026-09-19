package telegram

import (
	"sync"
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

func TestInMemoryRPCMetrics_ConcurrentObserveExactCounts(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	const goroutines = 16
	const perGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for worker := 0; worker < goroutines; worker++ {
		go func(worker int) {
			defer wg.Done()
			method := "messages.sendMessage"
			if worker%2 == 1 {
				method = "users.getFullUser"
			}
			for i := 0; i < perGoroutine; i++ {
				class := RPCSuccess
				if i%10 == 0 {
					class = RPCTransient
				}
				m.ObserveRequest(method, class, 1, time.Microsecond)
				m.ObserveWait("limiter", time.Microsecond)
				if i%20 == 0 {
					m.ObserveFloodWait(method, 2*time.Millisecond, i%40 == 0)
				}
			}
		}(worker)
	}
	wg.Wait()

	snap := m.Snapshot()
	wantRequests := int64(goroutines * perGoroutine)
	if snap.TotalRequests != wantRequests {
		t.Fatalf("total requests=%d, want %d", snap.TotalRequests, wantRequests)
	}
	if got := snap.RequestsByClass[RPCTransient]; got != wantRequests/10 {
		t.Fatalf("transient requests=%d, want %d", got, wantRequests/10)
	}
	if got := snap.RequestsByClass[RPCSuccess]; got != wantRequests-wantRequests/10 {
		t.Fatalf("success requests=%d, want %d", got, wantRequests-wantRequests/10)
	}
	if got := snap.RequestsByMethod["messages.sendMessage"]; got != wantRequests/2 {
		t.Fatalf("sendMessage requests=%d, want %d", got, wantRequests/2)
	}
	if got := snap.RequestsByMethod["users.getFullUser"]; got != wantRequests/2 {
		t.Fatalf("getFullUser requests=%d, want %d", got, wantRequests/2)
	}
	if got := snap.WaitTimeByScope["limiter"]; got != time.Duration(wantRequests)*time.Microsecond {
		t.Fatalf("limiter wait=%s, want %s", got, time.Duration(wantRequests)*time.Microsecond)
	}
	if snap.TotalWaitTime != time.Duration(wantRequests)*time.Microsecond {
		t.Fatalf("total wait=%s, want %s", snap.TotalWaitTime, time.Duration(wantRequests)*time.Microsecond)
	}

	wantFloods := wantRequests / 20
	if snap.FloodWaitCount != wantFloods {
		t.Fatalf("floodwait count=%d, want %d", snap.FloodWaitCount, wantFloods)
	}
	if snap.FloodWaitTotal != time.Duration(wantFloods)*2*time.Millisecond {
		t.Fatalf("floodwait total=%s, want %s", snap.FloodWaitTotal, time.Duration(wantFloods)*2*time.Millisecond)
	}
	if snap.FloodWaitDeferred != wantRequests/40 {
		t.Fatalf("deferred floodwait=%d, want %d", snap.FloodWaitDeferred, wantRequests/40)
	}
}

func TestInMemoryRPCMetrics_UnscopedWaitContributesToTotalOnly(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	m.ObserveWait("", 7*time.Millisecond)
	m.ObserveWait("limiter", 3*time.Millisecond)

	snap := m.Snapshot()
	if snap.TotalWaitTime != 10*time.Millisecond {
		t.Fatalf("total wait=%s, want 10ms", snap.TotalWaitTime)
	}
	if got := snap.WaitTimeByScope["limiter"]; got != 3*time.Millisecond {
		t.Fatalf("limiter wait=%s, want 3ms", got)
	}
	if _, ok := snap.WaitTimeByScope[""]; ok {
		t.Fatal("empty wait scope should not become a map label")
	}
}

func TestInMemoryRPCMetrics_ConcurrentSnapshot(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	const workers = 8
	const iterations = 2000

	var wg sync.WaitGroup
	wg.Add(workers + 1)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m.ObserveRequest("messages.getHistory", RPCSuccess, 1, time.Microsecond)
				m.ObserveWait("limiter", time.Nanosecond)
			}
		}(worker)
	}
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = m.Snapshot()
		}
	}()
	wg.Wait()

	snap := m.Snapshot()
	want := int64(workers * iterations)
	if snap.TotalRequests != want {
		t.Fatalf("total requests=%d, want %d", snap.TotalRequests, want)
	}
	if got := snap.RequestsByMethod["messages.getHistory"]; got != want {
		t.Fatalf("method requests=%d, want %d", got, want)
	}
}

func BenchmarkInMemoryRPCMetricsObserveRequestWarm(b *testing.B) {
	m := NewInMemoryRPCMetrics()
	m.ObserveRequest("messages.sendMessage", RPCSuccess, 1, time.Microsecond)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ObserveRequest("messages.sendMessage", RPCSuccess, 1, time.Microsecond)
	}
}

func BenchmarkInMemoryRPCMetricsObserveRequestWarmParallel(b *testing.B) {
	m := NewInMemoryRPCMetrics()
	m.ObserveRequest("messages.sendMessage", RPCSuccess, 1, time.Microsecond)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.ObserveRequest("messages.sendMessage", RPCSuccess, 1, time.Microsecond)
		}
	})
}

func TestInMemoryRPCMetrics_InvalidClassFallsBackToUnknown(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	m.ObserveRequest("test.invalid", RPCErrorClass(255), 1, time.Millisecond)

	snap := m.Snapshot()
	if snap.TotalRequests != 1 {
		t.Fatalf("total requests=%d, want 1", snap.TotalRequests)
	}
	if got := snap.RequestsByClass[RPCUnknown]; got != 1 {
		t.Fatalf("unknown class count=%d, want 1", got)
	}
	if got := snap.RequestsByMethod["test.invalid"]; got != 1 {
		t.Fatalf("method count=%d, want 1", got)
	}
}
