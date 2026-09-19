package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestLimiterCleanupWorkerIsZeroIdleAndRetires(t *testing.T) {
	l := New(Policy{Limit: 10, Window: time.Second, Burst: 10}, time.Second)
	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	l.lifecycleMu.Lock()
	if l.workerRunning {
		l.lifecycleMu.Unlock()
		t.Fatal("empty limiter started a cleanup goroutine")
	}
	l.lifecycleMu.Unlock()

	if !l.Allow(DimensionUser, "active") {
		t.Fatal("initial request rejected")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		l.lifecycleMu.Lock()
		running := l.workerRunning
		l.lifecycleMu.Unlock()
		if running {
			break
		}
		time.Sleep(time.Millisecond)
	}

	l.mu.Lock()
	for _, b := range l.buckets {
		b.lastAccess = time.Now().Add(-10 * time.Minute)
	}
	l.mu.Unlock()
	l.wakeCleanupWorker()

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		l.lifecycleMu.Lock()
		running := l.workerRunning
		l.lifecycleMu.Unlock()
		l.mu.RLock()
		count := len(l.buckets)
		l.mu.RUnlock()
		if !running && count == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	l.lifecycleMu.Lock()
	running := l.workerRunning
	l.lifecycleMu.Unlock()
	l.mu.RLock()
	count := len(l.buckets)
	l.mu.RUnlock()
	if running || count != 0 {
		t.Fatalf("cleanup did not retire: running=%v buckets=%d", running, count)
	}
	if err := l.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
