package client

import (
	"testing"
	"time"
)

func TestUserRateLimiterCardinalityIsBoundedFailClosed(t *testing.T) {
	limiter := NewUserRateLimiter(1, time.Hour)
	for i := 0; i < maxUserRateLimiterBuckets; i++ {
		if !limiter.Allow(int64(i+1), "command") {
			t.Fatalf("bucket %d unexpectedly rejected before capacity", i)
		}
	}
	if got := len(limiter.buckets); got != maxUserRateLimiterBuckets {
		t.Fatalf("unexpected assistant limiter cardinality: got=%d want=%d", got, maxUserRateLimiterBuckets)
	}
	if limiter.Allow(99_999_999, "command") {
		t.Fatal("new identity was admitted by evicting active limiter state")
	}

	limiter.mu.Lock()
	for _, bucket := range limiter.buckets {
		bucket.tokens = 0
		bucket.lastRefill = time.Now().Add(-2 * time.Hour)
		bucket.lastAccess = time.Now().Add(-2 * time.Hour)
		break
	}
	limiter.lastCapacitySweep = time.Time{}
	limiter.mu.Unlock()

	if !limiter.Allow(99_999_999, "command") {
		t.Fatalf("fully refilled idle bucket was not reclaimed; buckets=%d", len(limiter.buckets))
	}
	if got := len(limiter.buckets); got > maxUserRateLimiterBuckets {
		t.Fatalf("assistant limiter grew past cap: got=%d cap=%d", got, maxUserRateLimiterBuckets)
	}
}

func TestUserRateLimiterCategoryProfileKeepsInteractionBurstIndependent(t *testing.T) {
	limiter := NewUserRateLimiter(1, time.Hour)
	limiter.SetCategoryLimit("interaction", 3, time.Hour)

	for i := 0; i < 3; i++ {
		if !limiter.Allow(7, "interaction") {
			t.Fatalf("interaction burst rejected at token %d", i+1)
		}
	}
	if limiter.Allow(7, "interaction") {
		t.Fatal("interaction burst exceeded configured category capacity")
	}
	if !limiter.Allow(7, "command") {
		t.Fatal("interaction category unexpectedly consumed command capacity")
	}
	if limiter.Allow(7, "command") {
		t.Fatal("default command capacity was not preserved")
	}
}
