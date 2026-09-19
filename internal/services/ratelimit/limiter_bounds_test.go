package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterBucketCardinalityIsBoundedFailClosed(t *testing.T) {
	l := New(Policy{Limit: 1, Window: time.Second, Burst: 1}, time.Minute)

	for i := 0; i < defaultMaxBuckets; i++ {
		if !l.Allow(DimensionUser, fmt.Sprintf("user-%d", i)) {
			t.Fatalf("bucket %d unexpectedly rejected before capacity", i)
		}
	}
	if got := len(l.buckets); got != defaultMaxBuckets {
		t.Fatalf("unexpected bucket count: got=%d want=%d", got, defaultMaxBuckets)
	}
	if l.Allow(DimensionUser, "overflow") {
		t.Fatal("new bucket was admitted by evicting active rate-limit state")
	}
	if got := len(l.buckets); got != defaultMaxBuckets {
		t.Fatalf("bucket count changed after saturation: got=%d want=%d", got, defaultMaxBuckets)
	}

	l.mu.Lock()
	for _, b := range l.buckets {
		b.lastAccess = time.Now().Add(-10 * time.Minute)
		break
	}
	l.lastCapacitySweep = time.Time{}
	l.mu.Unlock()

	if !l.Allow(DimensionUser, "replacement") {
		t.Fatal("idle bucket was not reclaimed for a new key")
	}
	if got := len(l.buckets); got > defaultMaxBuckets {
		t.Fatalf("bucket count grew past cap after reclamation: got=%d cap=%d", got, defaultMaxBuckets)
	}
}
