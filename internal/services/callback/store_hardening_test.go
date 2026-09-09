package callback

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStateStoreSingleUseIsAtomic(t *testing.T) {
	s := NewStateStore()
	id := s.StoreWithScope("payload", StateScope{UserID: 42, ChatID: 99, MessageID: 7, Namespace: "test", SingleUse: true}, time.Minute)
	if id == "" {
		t.Fatal("expected opaque state ID")
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(id); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("expected exactly one successful consume, got %d", successes)
	}

	if _, err := s.GetEntry(id); !errors.Is(err, ErrStateConsumed) {
		t.Fatalf("expected consumed state, got %v", err)
	}
}

func TestStateStoreStartAcceptsNilContext(t *testing.T) {
	s := NewStateStore()
	_ = s.Start(nil)
	_ = s.Stop(nil)
}
