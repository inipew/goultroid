package inline

import (
	"context"
	"testing"
	"time"
)

func TestInlineCachePrunerIsZeroIdleAndRetires(t *testing.T) {
	cache := NewCache(time.Second)
	if err := cache.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	cache.mu.RLock()
	if cache.workerRunning {
		cache.mu.RUnlock()
		t.Fatal("empty inline cache started prune worker")
	}
	cache.mu.RUnlock()

	cache.SetScoped("zero-idle", nil, 10*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		cache.mu.RLock()
		running := cache.workerRunning
		entries := len(cache.entries)
		cache.mu.RUnlock()
		if !running && entries == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cache.mu.RLock()
	running := cache.workerRunning
	entries := len(cache.entries)
	cache.mu.RUnlock()
	if running || entries != 0 {
		t.Fatalf("pruner did not retire: running=%v entries=%d", running, entries)
	}
	if err := cache.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
