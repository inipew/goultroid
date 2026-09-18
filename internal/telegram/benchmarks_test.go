package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tgerr"
)

func BenchmarkRPCPolicyClassification(b *testing.B) {
	errFloodWait := &tgerr.Error{Code: 420, Message: "FLOOD_WAIT_10"}
	errStalePeer := &tgerr.Error{Code: 400, Message: "PEER_ID_INVALID"}
	errPermission := &tgerr.Error{Code: 403, Message: "CHAT_WRITE_FORBIDDEN"}
	errTransient := errors.New("read: connection reset by peer")

	errs := []error{errFloodWait, errStalePeer, errPermission, errTransient, nil}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ClassifyRPCError(errs[i%len(errs)])
	}
}

func BenchmarkRPCRetryBackoffCalculation(b *testing.B) {
	policy := DefaultRPCPolicy
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		attempt := (i % 5) + 1
		delay := policy.BaseDelay * time.Duration(1<<(attempt-1))
		if delay > policy.MaxDelay {
			delay = policy.MaxDelay
		}
		_ = delay
	}
}

func BenchmarkResolver_Self(b *testing.B) {
	r := NewResolver(nil, nil)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		peer, err := r.Resolve(ctx, "self")
		if err != nil || peer == nil {
			b.Fatalf("failed to resolve self: %v", err)
		}
	}
}

func BenchmarkResolverMemoryHit(b *testing.B) {
	r := NewResolver(nil, nil)
	r.cache.Set("user", "alice", "user", 12345, 67890)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		peer, id, err := r.ResolveUser(ctx, "alice")
		if err != nil || peer == nil || id != 12345 {
			b.Fatalf("failed to resolve: %v", err)
		}
	}
}

func BenchmarkPeerCache_GetSet(b *testing.B) {
	cache := NewPeerCache(ResolverCacheConfig{MaxEntries: 1000})
	cache.Set("user", "alice", "user", 12345, 67890)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get("user", "alice")
	}
}

func BenchmarkHierarchicalRPCLimiter_HotPeerAtHighCardinality(b *testing.B) {
	cfg := HierarchicalLimiterConfig{
		GlobalRate:         1e9,
		GlobalBurst:        1e9,
		DefaultFamilyRate:  1e9,
		DefaultFamilyBurst: 1e9,
		DefaultMethodRate:  1e9,
		DefaultMethodBurst: 1e9,
		DefaultPeerRate:    1e9,
		DefaultPeerBurst:   1e9,
		MaxBuckets:         DefaultMaxLimiterBuckets,
		MaxPenalties:       DefaultMaxLimiterPenalties,
		IdleTTL:            24 * time.Hour,
	}
	limiter := NewHierarchicalRPCLimiter(cfg)
	base := time.Unix(1_700_000_000, 0)

	// Populate near the production cardinality ceiling. The benchmarked hot
	// peer must remain O(request dimensions), independent of this population.
	for i := 0; i < 4000; i++ {
		dims := []LimitKey{{Scope: "peer", Key: fmt.Sprintf("peer:%d", i)}}
		if res := limiter.Reserve(base, dims, 1); !res.Allowed {
			b.Fatalf("failed to seed limiter at peer %d: %+v", i, res)
		}
	}
	hot := []LimitKey{
		{Scope: "global", Key: "account"},
		{Scope: "family", Key: "messages"},
		{Scope: "method", Key: "messages.sendMessage"},
		{Scope: "peer", Key: "peer:1"},
	}
	if res := limiter.Reserve(base, hot, 1); !res.Allowed {
		b.Fatalf("failed to initialize hot dimensions: %+v", res)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if res := limiter.Reserve(base.Add(time.Duration(i+1)*time.Nanosecond), hot, 1); !res.Allowed {
			b.Fatalf("hot reservation unexpectedly denied: %+v", res)
		}
	}
}
