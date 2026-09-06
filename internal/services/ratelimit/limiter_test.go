package ratelimit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/services/ratelimit"
)

func TestLimiter_BasicAllowAndExhaust(t *testing.T) {
	// Policy: 3 requests per 100ms, burst 3
	policy := ratelimit.Policy{
		Limit:  3,
		Window: 100 * time.Millisecond,
		Burst:  3,
	}

	l := ratelimit.New(policy, time.Minute)
	defer l.Close()

	key := "user-123"

	// 1. First 3 should succeed
	for i := 0; i < 3; i++ {
		if !l.Allow(ratelimit.DimensionUser, key) {
			t.Fatalf("expected request %d to be allowed", i+1)
		}
	}

	// 2. 4th should be denied
	res := l.Take(context.Background(), ratelimit.DimensionUser, key, 1)
	if res.Allowed {
		t.Fatalf("expected 4th request to be denied, but got allowed")
	}
	if res.RetryAfter <= 0 {
		t.Errorf("expected positive RetryAfter, got %v", res.RetryAfter)
	}

	// 3. Wait for refill window
	time.Sleep(120 * time.Millisecond)

	// Should be allowed again
	if !l.Allow(ratelimit.DimensionUser, key) {
		t.Fatalf("expected request after window refill to be allowed")
	}
}

func TestLimiter_DimensionSpecificPolicy(t *testing.T) {
	defaultPolicy := ratelimit.Policy{
		Limit:  10,
		Window: time.Minute,
		Burst:  10,
	}
	l := ratelimit.New(defaultPolicy, time.Minute)
	defer l.Close()

	// Set stricter command policy: burst 1
	l.SetPolicy(ratelimit.DimensionCommand, ratelimit.Policy{
		Limit:  1,
		Window: time.Hour,
		Burst:  1,
	})

	if !l.Allow(ratelimit.DimensionCommand, "play") {
		t.Fatalf("expected first play command to be allowed")
	}
	if l.Allow(ratelimit.DimensionCommand, "play") {
		t.Fatalf("expected second play command to be rate limited")
	}

	// But user dimension still uses default policy (burst 10)
	for i := 0; i < 5; i++ {
		if !l.Allow(ratelimit.DimensionUser, "user-456") {
			t.Fatalf("expected user request %d to be allowed under default policy", i+1)
		}
	}
}

func TestLimiter_Reset(t *testing.T) {
	policy := ratelimit.Policy{
		Limit:  1,
		Window: time.Hour,
		Burst:  1,
	}
	l := ratelimit.New(policy, time.Minute)
	defer l.Close()

	if !l.Allow(ratelimit.DimensionChat, "chat-1") {
		t.Fatalf("expected first chat request allowed")
	}
	if l.Allow(ratelimit.DimensionChat, "chat-1") {
		t.Fatalf("expected second chat request denied")
	}

	l.Reset(ratelimit.DimensionChat, "chat-1")

	if !l.Allow(ratelimit.DimensionChat, "chat-1") {
		t.Fatalf("expected request after Reset to be allowed")
	}
}

func TestLimiter_ConcurrentAccess(t *testing.T) {
	policy := ratelimit.Policy{
		Limit:  100,
		Window: time.Second,
		Burst:  100,
	}
	l := ratelimit.New(policy, time.Minute)
	defer l.Close()

	var wg sync.WaitGroup
	var allowedCount int
	var mu sync.Mutex

	workers := 20
	requestsPerWorker := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				if l.Allow(ratelimit.DimensionOperation, "concurrent-test") {
					mu.Lock()
					allowedCount++
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()

	if allowedCount > 100 {
		t.Fatalf("expected at most 100 allowed requests (burst limit), got %d", allowedCount)
	}
}
