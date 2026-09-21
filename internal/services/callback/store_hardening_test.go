package callback

import (
	"context"
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
			if _, err := s.claimEntry(id, nil); err == nil {
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

	if _, err := s.getEntry(id); !errors.Is(err, ErrStateConsumed) {
		t.Fatalf("expected consumed state, got %v", err)
	}
}

func TestStateStoreStartAcceptsNilContext(t *testing.T) {
	s := NewStateStore()
	_ = s.Start(nil)
	_ = s.Stop(nil)
}

func TestStateStore_MaxItemSizeEnforced(t *testing.T) {
	s := NewStateStore()
	// Exceeds 64KB
	hugePayload := make([]byte, 70*1024)
	id := s.Store(hugePayload, 1001, time.Minute)
	if id != "" {
		t.Fatalf("expected huge payload to be rejected with empty ID, got %s", id)
	}
	if s.Len() != 0 {
		t.Fatalf("expected store to remain empty, got len %d", s.Len())
	}
}

func TestStateStore_DefensiveCopy(t *testing.T) {
	s := NewStateStore()
	orig := []byte("hello-world")
	id := s.Store(orig, 1001, time.Minute)
	if id == "" {
		t.Fatal("expected successful store")
	}

	// Mutate original slice
	orig[0] = 'X'

	entry, err := s.getEntry(id)
	if err != nil {
		t.Fatalf("unexpected getEntry error: %v", err)
	}
	storedBytes, ok := entry.Data.([]byte)
	if !ok {
		t.Fatalf("expected []byte data, got %T", entry.Data)
	}
	if string(storedBytes) != "hello-world" {
		t.Fatalf("expected 'hello-world', got '%s' (store was corrupted by caller mutation)", string(storedBytes))
	}

	// Mutate returned slice
	storedBytes[0] = 'Y'

	entry2, err := s.getEntry(id)
	if err != nil {
		t.Fatalf("unexpected second getEntry error: %v", err)
	}
	if string(entry2.Data.([]byte)) != "hello-world" {
		t.Fatalf("expected 'hello-world', got '%s' (store was corrupted by return value mutation)", string(entry2.Data.([]byte)))
	}
}

func TestStateStore_DefensiveCopyNestedGraph(t *testing.T) {
	s := NewStateStore()
	original := map[string][]byte{"token": []byte("secret")}
	id := s.Store(original, 1001, time.Minute)
	if id == "" {
		t.Fatal("expected nested state to be accepted")
	}
	original["token"][0] = 'X'
	entry, err := s.getEntry(id)
	if err != nil {
		t.Fatalf("getEntry: %v", err)
	}
	got := entry.Data.(map[string][]byte)
	if string(got["token"]) != "secret" {
		t.Fatalf("input mutation reached store: %q", got["token"])
	}
	got["token"][0] = 'Y'
	entry, err = s.getEntry(id)
	if err != nil {
		t.Fatalf("second getEntry: %v", err)
	}
	if value := string(entry.Data.(map[string][]byte)["token"]); value != "secret" {
		t.Fatalf("returned mutation reached store: %q", value)
	}
}

func TestStateStore_RetainedBytesTrackingAndEviction(t *testing.T) {
	s := NewStateStore()
	payload := []byte("sample-data-payload")
	id := s.Store(payload, 1001, time.Minute)
	if id == "" {
		t.Fatal("expected successful store")
	}

	initialBytes := s.RetainedBytes()
	if initialBytes <= 0 {
		t.Fatalf("expected retainedBytes > 0, got %d", initialBytes)
	}

	s.deleteEntry(id)
	if remaining := s.RetainedBytes(); remaining != 0 {
		t.Fatalf("expected retainedBytes to be 0 after delete, got %d", remaining)
	}
}

func TestStateStoreLenPrunesExpiredWithoutBackgroundWorker(t *testing.T) {
	s := NewStateStore()
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	id := s.Store("short", 1, 5*time.Millisecond)
	if id == "" {
		t.Fatal("expected stored state")
	}
	time.Sleep(10 * time.Millisecond)
	if got := s.Len(); got != 0 {
		t.Fatalf("Len retained expired state without periodic worker: %d", got)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
