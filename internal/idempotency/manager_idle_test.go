package idempotency

import (
	"context"
	"testing"
	"time"
)

func TestIdempotencyCleanupWorkerIsZeroIdleAndRetires(t *testing.T) {
	mgr := NewManager(time.Second)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	mgr.lifecycleMu.Lock()
	if mgr.workerRunning {
		mgr.lifecycleMu.Unlock()
		t.Fatal("empty idempotency manager started cleanup worker")
	}
	mgr.lifecycleMu.Unlock()

	claimed, err := mgr.CheckAndSet(context.Background(), "zero-idle", 10*time.Millisecond)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mgr.lifecycleMu.Lock()
		running := mgr.workerRunning
		mgr.lifecycleMu.Unlock()
		if !running {
			if size := mgr.Size(); size == 0 {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}

	mgr.lifecycleMu.Lock()
	running := mgr.workerRunning
	mgr.lifecycleMu.Unlock()
	if size := mgr.Size(); running || size != 0 {
		t.Fatalf("cleanup did not retire: running=%v size=%d", running, size)
	}
	if err := mgr.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
