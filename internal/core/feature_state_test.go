package core

import (
	"sync"
	"testing"
)

func TestChatFeatureSnapshotFailsOpenUntilLoaded(t *testing.T) {
	var snapshot ChatFeatureSnapshot
	if !snapshot.Interested(10) {
		t.Fatal("unloaded snapshot must fail open")
	}
	snapshot.SetActive(10, true)
	if !snapshot.Interested(999) {
		t.Fatal("mutation before preload must not create an incomplete known snapshot")
	}
}

func TestChatFeatureSnapshotTracksActiveAndUnknownChats(t *testing.T) {
	var snapshot ChatFeatureSnapshot
	snapshot.ReplaceLoaded([]int64{10, 20})

	if !snapshot.Interested(10) || !snapshot.Interested(20) {
		t.Fatal("loaded active chats were not interested")
	}
	if snapshot.Interested(30) {
		t.Fatal("inactive loaded chat unexpectedly interested")
	}

	snapshot.SetActive(30, true)
	if !snapshot.Interested(30) {
		t.Fatal("newly active chat not reflected")
	}
	snapshot.SetActive(10, false)
	if snapshot.Interested(10) {
		t.Fatal("inactive chat remained interested")
	}

	snapshot.MarkUnknown(10)
	if !snapshot.Interested(10) {
		t.Fatal("unknown chat must fail open")
	}
	if snapshot.Interested(40) {
		t.Fatal("unknown state for one chat must not invalidate all chats")
	}
}

func TestChatFeatureSnapshotConcurrentMutationDoesNotLoseUpdates(t *testing.T) {
	var snapshot ChatFeatureSnapshot
	snapshot.ReplaceLoaded(nil)

	var wg sync.WaitGroup
	for i := int64(1); i <= 32; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot.SetActive(i, true)
		}()
	}
	wg.Wait()

	for i := int64(1); i <= 32; i++ {
		if !snapshot.Interested(i) {
			t.Fatalf("active chat %d lost during concurrent snapshot update", i)
		}
	}
}

func BenchmarkChatFeatureSnapshotInterested(b *testing.B) {
	var snapshot ChatFeatureSnapshot
	snapshot.ReplaceLoaded([]int64{10, 20, 30})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if snapshot.Interested(999) {
			b.Fatal("inactive chat unexpectedly interested")
		}
	}
}
