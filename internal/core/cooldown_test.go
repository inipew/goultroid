package core

import (
	"sync"
	"testing"
	"time"
)

func TestCooldownTracker(t *testing.T) {
	tracker := NewCooldownTracker()

	userID := int64(12345)
	cmd := "ping"
	duration := 100 * time.Millisecond

	// 1. First execution should succeed
	rem, ok := tracker.CheckAndRecord(userID, cmd, duration)
	if !ok || rem != 0 {
		t.Fatalf("expected first execution to succeed, got ok=%v rem=%v", ok, rem)
	}

	// 2. Immediate second execution should be blocked by cooldown
	rem, ok = tracker.CheckAndRecord(userID, cmd, duration)
	if ok || rem <= 0 {
		t.Fatalf("expected second execution to be on cooldown, got ok=%v rem=%v", ok, rem)
	}

	// 3. Different user executing the same command should succeed
	rem, ok = tracker.CheckAndRecord(int64(99999), cmd, duration)
	if !ok || rem != 0 {
		t.Fatalf("expected different user to succeed, got ok=%v rem=%v", ok, rem)
	}

	// 4. Different command for the same user should succeed
	rem, ok = tracker.CheckAndRecord(userID, "alive", duration)
	if !ok || rem != 0 {
		t.Fatalf("expected different command to succeed, got ok=%v rem=%v", ok, rem)
	}

	// 5. After duration expires, execution should succeed again
	time.Sleep(110 * time.Millisecond)
	rem, ok = tracker.CheckAndRecord(userID, cmd, duration)
	if !ok || rem != 0 {
		t.Fatalf("expected execution after cooldown expiration to succeed, got ok=%v rem=%v", ok, rem)
	}

	// 6. Nil tracker safety
	var nilTracker *CooldownTracker
	rem, ok = nilTracker.CheckAndRecord(userID, cmd, duration)
	if !ok || rem != 0 {
		t.Errorf("expected nil tracker to return ok=true")
	}

	// 7. Zero duration safety
	rem, ok = tracker.CheckAndRecord(userID, cmd, 0)
	if !ok || rem != 0 {
		t.Errorf("expected 0 duration to return ok=true")
	}
}

func TestCooldownTracker_Concurrent(t *testing.T) {
	tracker := NewCooldownTracker()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			tracker.CheckAndRecord(id, "concurrentCmd", 50*time.Millisecond)
		}(int64(i % 5))
	}

	wg.Wait()
}

func TestCooldownTracker_Cleanup(t *testing.T) {
	tracker := NewCooldownTracker()

	tracker.CheckAndRecord(101, "cmd1", time.Second)
	tracker.CheckAndRecord(102, "cmd2", time.Second)

	// Immediately, cleanup with 1 hour maxAge should purge nothing
	purged := tracker.Cleanup(1 * time.Hour)
	if purged != 0 {
		t.Errorf("expected 0 purged, got %d", purged)
	}

	// Artificially set an old timestamp
	tracker.mu.Lock()
	tracker.records[userCommandKey{userID: 999, cmdName: "old"}] = cooldownRecord{
		at: time.Now().Add(-2 * time.Hour), duration: time.Second,
	}
	tracker.mu.Unlock()

	purged = tracker.Cleanup(1 * time.Hour)
	if purged != 1 {
		t.Errorf("expected 1 purged record, got %d", purged)
	}

	// Nil safety
	var nilTracker *CooldownTracker
	if n := nilTracker.Cleanup(time.Hour); n != 0 {
		t.Errorf("expected 0 from nil tracker cleanup, got %d", n)
	}
}

func TestCooldownTrackerCardinalityIsBoundedFailClosed(t *testing.T) {
	tracker := NewCooldownTracker()
	for i := 0; i < maxCooldownRecords; i++ {
		if _, ok := tracker.CheckAndRecord(int64(i+1), "bounded", time.Hour); !ok {
			t.Fatalf("entry %d unexpectedly rejected before capacity", i)
		}
	}
	if got := len(tracker.records); got != maxCooldownRecords {
		t.Fatalf("unexpected cooldown cardinality: got=%d want=%d", got, maxCooldownRecords)
	}
	if _, ok := tracker.CheckAndRecord(9_999_999, "bounded", time.Hour); ok {
		t.Fatal("new cooldown identity was admitted by evicting active state")
	}

	tracker.mu.Lock()
	for key := range tracker.records {
		tracker.records[key] = cooldownRecord{
			at: time.Now().Add(-2 * time.Hour), duration: time.Second,
		}
		break
	}
	tracker.lastCapacitySweep = time.Time{}
	tracker.mu.Unlock()

	if _, ok := tracker.CheckAndRecord(9_999_999, "bounded", time.Hour); !ok {
		t.Fatal("expired cooldown state was not reclaimed")
	}
	if got := len(tracker.records); got > maxCooldownRecords {
		t.Fatalf("cooldown cardinality grew past cap: got=%d cap=%d", got, maxCooldownRecords)
	}
}
