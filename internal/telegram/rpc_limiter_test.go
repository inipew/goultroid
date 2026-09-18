package telegram

import (
	"testing"
	"time"
)

func TestHierarchicalRPCLimiter_AtomicRejection(t *testing.T) {
	cfg := HierarchicalLimiterConfig{
		GlobalRate:         10,
		GlobalBurst:        10,
		DefaultFamilyRate:  10,
		DefaultFamilyBurst: 10,
		DefaultPeerRate:    1,
		DefaultPeerBurst:   1, // Only 1 token for peer
	}
	limiter := NewHierarchicalRPCLimiter(cfg)
	now := time.Now()

	dims := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "peer", Key: "user:123"},
	}

	// First request costs 1: should succeed
	res1 := limiter.Reserve(now, dims, 1)
	if !res1.Allowed {
		t.Fatalf("expected res1 allowed, got false")
	}
	if res1.RetryAfter != 0 {
		t.Fatalf("expected res1 retryAfter 0, got %v", res1.RetryAfter)
	}

	// Second request immediately after: peer has 0 tokens left, so it should be rejected
	res2 := limiter.Reserve(now, dims, 1)
	if res2.Allowed {
		t.Fatalf("expected res2 rejected due to exhausted peer tokens, got allowed")
	}
	if res2.RetryAfter <= 0 {
		t.Fatalf("expected res2 retryAfter > 0, got %v", res2.RetryAfter)
	}

	// Check that another peer can still make a request (global wasn't drained)
	dimsPeer2 := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "peer", Key: "user:456"},
	}
	res3 := limiter.Reserve(now, dimsPeer2, 1)
	if !res3.Allowed {
		t.Fatalf("peer2 should succeed because global and family still had tokens, got rejected")
	}
}

func TestHierarchicalRPCLimiter_FloodWaitPenalty(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(DefaultHierarchicalLimiterConfig())
	now := time.Now()

	dims := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "peer", Key: "user:123"},
	}

	// Penalize peer:123 with 5 seconds
	limiter.Penalize(now, []LimitKey{{Scope: "peer", Key: "user:123"}}, 5*time.Second)

	// Immediate reserve should be rejected with ~5s RetryAfter
	res := limiter.Reserve(now, dims, 1)
	if res.Allowed {
		t.Fatalf("expected res rejected by floodwait penalty")
	}
	if res.RetryAfter != 5*time.Second {
		t.Fatalf("expected retryAfter 5s, got %v", res.RetryAfter)
	}

	// After 2 seconds, remaining wait should be ~3s
	res2 := limiter.Reserve(now.Add(2*time.Second), dims, 1)
	if res2.Allowed {
		t.Fatalf("expected res2 rejected by floodwait penalty")
	}
	if res2.RetryAfter != 3*time.Second {
		t.Fatalf("expected retryAfter 3s, got %v", res2.RetryAfter)
	}

	// Different peer should NOT be penalized
	dimsOther := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "peer", Key: "user:999"},
	}
	resOther := limiter.Reserve(now, dimsOther, 1)
	if !resOther.Allowed {
		t.Fatalf("expected other peer allowed, got rejected: %v", resOther.RetryAfter)
	}

	// After 5 seconds + 1ms, original peer should be allowed
	res3 := limiter.Reserve(now.Add(5*time.Second+time.Millisecond), dims, 1)
	if !res3.Allowed {
		t.Fatalf("expected res3 allowed after penalty expiry, got rejected: %v", res3.RetryAfter)
	}
}

func TestHierarchicalRPCLimiter_HardBounds(t *testing.T) {
	const maxB = 10
	const maxP = 10
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:   100,
		GlobalBurst:  100,
		MaxBuckets:   maxB,
		MaxPenalties: maxP,
	})
	now := time.Now()

	// 1. Test Buckets Hard Bound
	for i := 0; i < 50; i++ {
		dims := []LimitKey{
			{Scope: "global", Key: "account"},
			{Scope: "peer", Key: string(rune('a' + i))},
		}
		_ = limiter.Reserve(now, dims, 1)
	}
	buckets, _ := limiter.Size()
	if buckets > maxB {
		t.Fatalf("buckets count %d exceeded max hard bound %d", buckets, maxB)
	}

	// 2. Test Penalties Hard Bound
	for i := 0; i < 50; i++ {
		limiter.Penalize(now, []LimitKey{{Scope: "peer", Key: string(rune('z' - i))}}, time.Duration(i+1)*time.Minute)
	}
	_, penalties := limiter.Size()
	if penalties > maxP {
		t.Fatalf("penalties count %d exceeded max hard bound %d", penalties, maxP)
	}

	// 3. Test MaxPenaltyDuration clamp
	limiter.Penalize(now, []LimitKey{{Scope: "peer", Key: "forever"}}, 100*24*time.Hour)
	res := limiter.Reserve(now, []LimitKey{{Scope: "peer", Key: "forever"}}, 1)
	if res.Allowed {
		t.Fatalf("expected penalized peer to be disallowed")
	}
	if res.RetryAfter > MaxPenaltyDuration {
		t.Fatalf("penalty duration %v was not clamped to MaxPenaltyDuration %v", res.RetryAfter, MaxPenaltyDuration)
	}
}
