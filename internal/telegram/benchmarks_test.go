package telegram

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/execution"
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

func BenchmarkHierarchicalRPCLimiterCardinality(b *testing.B) {
	for _, cardinality := range []int{1, 100, 1000, DefaultMaxLimiterBuckets} {
		b.Run(fmt.Sprintf("buckets_%d", cardinality), func(b *testing.B) {
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

			var hot []LimitKey
			seedCount := 0
			if cardinality == 1 {
				hot = []LimitKey{{Scope: "peer", Key: "peer:hot"}}
			} else {
				hot = []LimitKey{
					{Scope: "global", Key: "account"},
					{Scope: "family", Key: "messages"},
					{Scope: "method", Key: "messages.sendMessage"},
					{Scope: "peer", Key: "peer:hot"},
				}
				seedCount = cardinality - len(hot)
			}
			for i := 0; i < seedCount; i++ {
				if res := limiter.Reserve(base, []LimitKey{{Scope: "peer", Key: fmt.Sprintf("seed:%d", i)}}, 1); !res.Allowed {
					b.Fatalf("seed %d/%d denied: %+v", i, seedCount, res)
				}
			}
			if res := limiter.Reserve(base, hot, 1); !res.Allowed {
				b.Fatalf("hot dimensions denied during setup: %+v", res)
			}
			if buckets, _ := limiter.Size(); buckets != cardinality {
				b.Fatalf("resident buckets=%d, want %d", buckets, cardinality)
			}

			b.ReportAllocs()
			b.ReportMetric(float64(cardinality), "resident_buckets")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				now := base.Add(time.Duration(i+1) * time.Nanosecond)
				if res := limiter.Reserve(now, hot, 1); !res.Allowed {
					b.Fatalf("hot reservation denied at iteration %d: %+v", i, res)
				}
			}
		})
	}
}

type benchmarkOccupancySleeper struct {
	calls     atomic.Int64
	waitNanos atomic.Int64
}

func (s *benchmarkOccupancySleeper) Sleep(ctx context.Context, d time.Duration) error {
	s.calls.Add(1)
	s.waitNanos.Add(int64(d))
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func BenchmarkRPCExecutorSamePeerFloodWaitOccupancy(b *testing.B) {
	for _, durable := range []bool{false, true} {
		mode := "interactive"
		if durable {
			mode = "durable"
		}
		for _, concurrency := range []int{1, 100, 1000} {
			b.Run(fmt.Sprintf("%s/concurrency_%d", mode, concurrency), func(b *testing.B) {
				sleeper := &benchmarkOccupancySleeper{}
				exec, err := NewRPCExecutor(RPCExecutorConfig{
					Limiter: NoopRPCLimiter{},
					Sleeper: sleeper,
					DefaultPolicy: RetryPolicy{
						MaxAttempts:        2,
						BaseDelay:          time.Millisecond,
						MaxDelay:           time.Millisecond,
						MaxElapsed:         5 * time.Second,
						InlineFloodWaitMax: 5 * time.Second,
					},
				})
				if err != nil {
					b.Fatal(err)
				}

				var physicalCalls atomic.Int64
				var failures atomic.Int64
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					var wg sync.WaitGroup
					wg.Add(concurrency)
					for worker := 0; worker < concurrency; worker++ {
						go func() {
							defer wg.Done()
							ctx := context.Background()
							if durable {
								ctx = execution.WithMetadata(ctx, execution.Metadata{CanDurablyYield: true})
							}
							calls := 0
							err := exec.Do(ctx, RPCMeta{
								Method:  "messages.sendMessage",
								Family:  "messages",
								PeerKey: "peer:same",
								Kind:    RPCReadOnly,
							}, func(context.Context) error {
								calls++
								physicalCalls.Add(1)
								if calls == 1 {
									return tgerr.New(420, "FLOOD_WAIT_1")
								}
								return nil
							})
							if durable {
								if err == nil {
									failures.Add(1)
								}
							} else if err != nil {
								failures.Add(1)
							}
						}()
					}
					wg.Wait()
				}
				b.StopTimer()

				if failures.Load() != 0 {
					b.Fatalf("unexpected RPC outcomes=%d", failures.Load())
				}
				logicalOps := float64(b.N * concurrency)
				if logicalOps > 0 {
					b.ReportMetric(float64(physicalCalls.Load())/logicalOps, "physical-rpcs/op")
					b.ReportMetric(float64(sleeper.calls.Load())/logicalOps, "inline-sleeps/op")
					b.ReportMetric(float64(sleeper.waitNanos.Load())/logicalOps, "inline-wait-ns/op")
				}
			})
		}
	}
}

func BenchmarkPeerRPCLimitKey(b *testing.B) {
	peer := &tg.InputPeerUser{UserID: 123456789, AccessHash: 987654321}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := peerRPCLimitKey(peer)
		if key.Scope != "peer" || key.Key != "user" || key.ID != peer.UserID {
			b.Fatal(key)
		}
	}
}

func BenchmarkServiceSinglePeerWrapperFastPath(b *testing.B) {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter: NoopRPCLimiter{},
		DefaultPolicy: RetryPolicy{
			MaxAttempts: 1,
			MaxElapsed:  time.Second,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	svc := NewServiceWithExecutor(nil, exec)
	peer := &tg.InputPeerUser{UserID: 42, AccessHash: 99}
	ctx := context.Background()
	op := func(context.Context, tg.InputPeerClass) error { return nil }

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := svc.execReadOnlyPeer(ctx, "users.getFullUser", peer, op); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHierarchicalRPCLimiterSafePeerReclamation(b *testing.B) {
	for _, cardinality := range []int{1000, DefaultMaxLimiterBuckets} {
		b.Run(fmt.Sprintf("peers_%d", cardinality), func(b *testing.B) {
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				limiter := NewHierarchicalRPCLimiter(HierarchicalLimiterConfig{
					GlobalRate:         1e9,
					GlobalBurst:        1e9,
					DefaultFamilyRate:  1e9,
					DefaultFamilyBurst: 1e9,
					DefaultMethodRate:  1e9,
					DefaultMethodBurst: 1e9,
					DefaultPeerRate:    1000,
					DefaultPeerBurst:   1,
					MaxBuckets:         cardinality + 1,
					MaxPenalties:       16,
					IdleTTL:            10 * time.Minute,
				})
				now := time.Unix(1_700_000_000, 0)
				for peer := 0; peer < cardinality; peer++ {
					if res := limiter.Reserve(now, []LimitKey{{Scope: "peer", Key: "user", ID: int64(peer + 1)}}, 1); !res.Allowed {
						b.Fatalf("seed peer %d failed: %+v", peer, res)
					}
				}

				b.StartTimer()
				trigger := []LimitKey{{Scope: "peer", Key: "user", ID: int64(cardinality + 1)}}
				if res := limiter.Reserve(now.Add(time.Millisecond), trigger, 1); !res.Allowed {
					b.Fatalf("trigger reservation failed: %+v", res)
				}
				b.StopTimer()

				if buckets, _ := limiter.Size(); buckets != 1 {
					b.Fatalf("resident buckets=%d after safe reclaim, want 1", buckets)
				}
			}
			b.ReportAllocs()
		})
	}
}
