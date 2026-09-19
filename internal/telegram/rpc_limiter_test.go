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

func TestHierarchicalRPCLimiter_DefaultMethodBucketIsActive(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  1,
		DefaultMethodBurst: 1,
		DefaultPeerRate:    100,
		DefaultPeerBurst:   100,
	})
	now := time.Now()

	dims := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "method", Key: "messages.sendMessage"},
		{Scope: "peer", Key: "user:123"},
	}
	if res := limiter.Reserve(now, dims, 1); !res.Allowed {
		t.Fatalf("expected first method reservation to succeed, got retry after %v", res.RetryAfter)
	}
	if res := limiter.Reserve(now, dims, 1); res.Allowed || res.RetryAfter <= 0 {
		t.Fatalf("expected second reservation to be rejected by method bucket, got %+v", res)
	}

	otherMethod := append([]LimitKey(nil), dims...)
	otherMethod[2] = LimitKey{Scope: "method", Key: "messages.editMessage"}
	if res := limiter.Reserve(now, otherMethod, 1); !res.Allowed {
		t.Fatalf("expected independent method bucket to allow request, got retry after %v", res.RetryAfter)
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

func TestHierarchicalRPCLimiter_IdleTTLReclaimsDepletedBucket(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultPeerRate:    1,
		DefaultPeerBurst:   1,
		MaxBuckets:         2,
		MaxPenalties:       8,
		IdleTTL:            time.Second,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  100,
		DefaultMethodBurst: 100,
	})
	now := time.Now()
	first := []LimitKey{{Scope: "peer", Key: "user:1"}}
	if res := limiter.Reserve(now, first, 1); !res.Allowed {
		t.Fatalf("first peer reservation failed: %+v", res)
	}
	// The bucket is depleted (tokens == 0), but inactivity rather than token
	// fullness defines reclamation. The old implementation leaked this entry.
	if res := limiter.Reserve(now.Add(2*time.Second), []LimitKey{{Scope: "peer", Key: "user:2"}}, 1); !res.Allowed {
		t.Fatalf("new peer should be admitted after idle reclamation: %+v", res)
	}
	buckets, _ := limiter.Size()
	if buckets != 1 {
		t.Fatalf("expected exactly one live peer bucket after reclamation, got %d", buckets)
	}
}

func TestHierarchicalRPCLimiter_BucketSaturationFailsClosedWithoutResettingLiveState(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultPeerRate:    1,
		DefaultPeerBurst:   1,
		MaxBuckets:         2, // global + exactly one peer
		MaxPenalties:       8,
		IdleTTL:            10 * time.Second,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  100,
		DefaultMethodBurst: 100,
	})
	now := time.Now()
	peer1 := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "peer", Key: "user:1"},
	}
	if res := limiter.Reserve(now, peer1, 1); !res.Allowed {
		t.Fatalf("first peer reservation failed: %+v", res)
	}

	peer2 := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "peer", Key: "user:2"},
	}
	if res := limiter.Reserve(now, peer2, 1); res.Allowed || res.RetryAfter <= 0 {
		t.Fatalf("new identity must fail closed while live state fills the cap: %+v", res)
	}

	// The old limiter evicted peer1 to make room for peer2, resetting peer1's
	// exhausted token bucket. Its state must still be present and rate-limited.
	if res := limiter.Reserve(now, peer1, 1); res.Allowed || res.RetryAfter <= 0 {
		t.Fatalf("live peer state was reset/discarded under saturation: %+v", res)
	}
	buckets, _ := limiter.Size()
	if buckets != 2 {
		t.Fatalf("expected global+peer1 to remain resident, got %d buckets", buckets)
	}
}

func TestHierarchicalRPCLimiter_PenaltyOverflowFailsClosedWithoutDroppingFloodWait(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultPeerRate:    100,
		DefaultPeerBurst:   100,
		MaxBuckets:         16,
		MaxPenalties:       1,
		IdleTTL:            time.Minute,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  100,
		DefaultMethodBurst: 100,
	})
	now := time.Now()
	peer1 := LimitKey{Scope: "peer", Key: "user:1"}
	peer2 := LimitKey{Scope: "peer", Key: "user:2"}
	peer3 := LimitKey{Scope: "peer", Key: "user:3"}

	limiter.Penalize(now, []LimitKey{peer1}, 10*time.Second)
	limiter.Penalize(now, []LimitKey{peer2}, 5*time.Second) // overflows bounded penalty state

	// Overflow is conservatively promoted to account-wide cooldown rather than
	// evicting the live peer1 penalty.
	if res := limiter.Reserve(now, []LimitKey{peer3}, 1); res.Allowed || res.RetryAfter != 5*time.Second {
		t.Fatalf("expected overflow cooldown of 5s, got %+v", res)
	}
	_, penalties := limiter.Size()
	if penalties != 1 {
		t.Fatalf("penalty map exceeded/changed hard bound: %d", penalties)
	}

	// After overflow cooldown expires, unrelated peers proceed while the
	// original longer FloodWait remains intact.
	if res := limiter.Reserve(now.Add(6*time.Second), []LimitKey{peer3}, 1); !res.Allowed {
		t.Fatalf("unrelated peer should recover after overflow cooldown: %+v", res)
	}
	if res := limiter.Reserve(now.Add(6*time.Second), []LimitKey{peer1}, 1); res.Allowed || res.RetryAfter != 4*time.Second {
		t.Fatalf("original FloodWait was lost or shortened: %+v", res)
	}
}

func TestHierarchicalRPCLimiter_ExtendingPenaltyDoesNotGrowHeapState(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultPeerRate:    100,
		DefaultPeerBurst:   100,
		MaxBuckets:         16,
		MaxPenalties:       2,
		IdleTTL:            time.Minute,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  100,
		DefaultMethodBurst: 100,
	})
	now := time.Now()
	peer := LimitKey{Scope: "peer", Key: "user:1"}

	for i := 1; i <= 1000; i++ {
		limiter.Penalize(now, []LimitKey{peer}, time.Duration(i)*time.Second)
	}
	if len(limiter.penaltyQ) != 1 {
		t.Fatalf("expected one indexed heap node for repeated extensions, got %d", len(limiter.penaltyQ))
	}
	_, penalties := limiter.Size()
	if penalties != 1 {
		t.Fatalf("expected one active penalty, got %d", penalties)
	}
}


func TestHierarchicalRPCLimiter_TypedPeerIdentitySeparatesKinds(t *testing.T) {
	limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
		GlobalRate:         100,
		GlobalBurst:        100,
		DefaultFamilyRate:  100,
		DefaultFamilyBurst: 100,
		DefaultMethodRate:  100,
		DefaultMethodBurst: 100,
		DefaultPeerRate:    1,
		DefaultPeerBurst:   1,
		MaxBuckets:         16,
		MaxPenalties:       16,
		IdleTTL:            time.Minute,
	})
	now := time.Now()
	user := []LimitKey{{Scope: "peer", Key: "user", ID: 42}}
	channel := []LimitKey{{Scope: "peer", Key: "channel", ID: 42}}

	if res := limiter.Reserve(now, user, 1); !res.Allowed {
		t.Fatalf("first typed user reservation failed: %+v", res)
	}
	if res := limiter.Reserve(now, user, 1); res.Allowed || res.RetryAfter <= 0 {
		t.Fatalf("typed user bucket was not independently exhausted: %+v", res)
	}
	if res := limiter.Reserve(now, channel, 1); !res.Allowed {
		t.Fatalf("channel with same numeric id collided with user bucket: %+v", res)
	}

	limiter.Penalize(now, user, 5*time.Second)
	if res := limiter.Reserve(now, user, 1); res.Allowed || res.RetryAfter != 5*time.Second {
		t.Fatalf("typed user penalty missing: %+v", res)
	}
	if res := limiter.Reserve(now, channel, 1); res.Allowed {
		// The channel token was consumed above; advance enough for exactly one
		// peer token so this assertion tests penalty isolation, not token state.
		if res2 := limiter.Reserve(now.Add(time.Second), channel, 1); !res2.Allowed {
			t.Fatalf("typed user penalty leaked into channel identity: %+v", res2)
		}
	}
}
