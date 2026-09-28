package telegram

import (
	"fmt"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

const (
	t10PeerCacheEntries = 256
	t10LimiterBuckets   = 128
	t10LimiterPenalties = 32
)

type t10ProcessSample struct {
	Goroutines int
	HeapAlloc  uint64
}

func t10SampleProcess() t10ProcessSample {
	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	return t10ProcessSample{
		Goroutines: goruntime.NumGoroutine(),
		HeapAlloc:  mem.HeapAlloc,
	}
}

func TestT10TelegramBoundedStateHighCardinalityAcceptance(t *testing.T) {
	baseline := t10SampleProcess()

	svc := NewService(nil)
	peer := &tg.InputPeerChat{ChatID: 42}
	base := time.Unix(1_700_000_000, 0)
	for msgID := 1; msgID <= botSentCapacity*100; msgID++ {
		svc.recordBotSentAt(peer, msgID, base.Add(time.Duration(msgID)*time.Nanosecond))
	}
	svc.botSentMu.RLock()
	botEntries := len(svc.botSentMessages)
	botOrder := svc.botSentOrder.Len()
	svc.botSentMu.RUnlock()
	if botEntries != botSentCapacity || botOrder != botSentCapacity {
		t.Fatalf("bot-origin retained entries/order=%d/%d, want %d/%d", botEntries, botOrder, botSentCapacity, botSentCapacity)
	}

	cache := NewPeerCache(ResolverCacheConfig{
		MaxEntries:  t10PeerCacheEntries,
		PositiveTTL: time.Hour,
		NegativeTTL: time.Hour,
	})
	for i := 0; i < t10PeerCacheEntries*100; i++ {
		key := fmt.Sprintf("peer-%05d", i)
		if i%3 == 0 {
			cache.SetNegative("user", key)
		} else {
			cache.Set("user", key, "user", int64(i+1), int64(i+1000))
		}
		if i%5 == 0 {
			cache.Invalidate("user", key)
			cache.Set("user", key, "user", int64(i+1), int64(i+2000))
		}
	}
	cache.mu.RLock()
	cacheEntries := len(cache.entries)
	cacheOrder := cache.order.Len()
	cacheIndex := len(cache.orderIndex)
	cache.mu.RUnlock()
	if cacheEntries > t10PeerCacheEntries {
		t.Fatalf("peer cache retained entries=%d, cap=%d", cacheEntries, t10PeerCacheEntries)
	}
	if cacheOrder != cacheEntries || cacheIndex != cacheEntries {
		t.Fatalf("peer cache entries/order/index=%d/%d/%d, want one ordering node per live entry", cacheEntries, cacheOrder, cacheIndex)
	}

	cfg := DefaultHierarchicalLimiterConfig()
	cfg.MaxBuckets = t10LimiterBuckets
	cfg.MaxPenalties = t10LimiterPenalties
	cfg.IdleTTL = time.Hour
	limiter := NewHierarchicalRPCLimiter(cfg)
	for i := 0; i < t10LimiterBuckets; i++ {
		reservation := limiter.Reserve(base, []LimitKey{{Scope: "peer", Key: "user", ID: int64(i + 1)}}, 1)
		if !reservation.Allowed {
			t.Fatalf("limiter rejected seed bucket %d: %+v", i, reservation)
		}
	}
	if reservation := limiter.Reserve(base, []LimitKey{{Scope: "peer", Key: "user", ID: 999999}}, 1); reservation.Allowed {
		t.Fatal("limiter admitted a new live bucket beyond hard capacity")
	}
	for i := 0; i < t10LimiterPenalties*100; i++ {
		limiter.Penalize(base, []LimitKey{{Scope: "peer", Key: "penalty", ID: int64(i + 1)}}, time.Minute)
	}
	buckets, penalties := limiter.Size()
	if buckets > t10LimiterBuckets || penalties > t10LimiterPenalties {
		t.Fatalf("limiter retained buckets/penalties=%d/%d, caps=%d/%d", buckets, penalties, t10LimiterBuckets, t10LimiterPenalties)
	}

	goruntime.GC()
	goruntime.Gosched()
	settled := t10SampleProcess()
	t.Logf(
		"T10 Telegram bounds bot=%d order=%d peer_cache=%d order=%d index=%d limiter_buckets=%d penalties=%d baseline=%+v settled=%+v",
		botEntries,
		botOrder,
		cacheEntries,
		cacheOrder,
		cacheIndex,
		buckets,
		penalties,
		baseline,
		settled,
	)
}

func BenchmarkT10BotOriginSteadyStateBurst(b *testing.B) {
	svc := NewService(nil)
	peer := &tg.InputPeerChat{ChatID: 42}
	base := time.Unix(1_700_000_000, 0)
	for msgID := 1; msgID <= botSentCapacity; msgID++ {
		svc.recordBotSentAt(peer, msgID, base.Add(time.Duration(msgID)*time.Nanosecond))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		svc.recordBotSentAt(peer, botSentCapacity+i+1, base.Add(time.Duration(botSentCapacity+i+1)*time.Nanosecond))
	}
	b.StopTimer()

	svc.botSentMu.RLock()
	entries := len(svc.botSentMessages)
	ordered := svc.botSentOrder.Len()
	svc.botSentMu.RUnlock()
	if entries != botSentCapacity || ordered != botSentCapacity {
		b.Fatalf("bot-origin retained entries/order=%d/%d, want %d/%d", entries, ordered, botSentCapacity, botSentCapacity)
	}
	b.ReportMetric(float64(entries), "resident_entries")
	b.ReportMetric(float64(ordered), "ordering_nodes")
}

func BenchmarkT10PeerCacheBoundedChurn(b *testing.B) {
	for _, maxEntries := range []int{256, 4096} {
		b.Run(fmt.Sprintf("entries_%d", maxEntries), func(b *testing.B) {
			cache := NewPeerCache(ResolverCacheConfig{
				MaxEntries:  maxEntries,
				PositiveTTL: time.Hour,
				NegativeTTL: time.Hour,
			})
			keys := make([]string, maxEntries*2)
			for i := range keys {
				keys[i] = fmt.Sprintf("peer-%05d", i)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				slot := i % len(keys)
				key := keys[slot]
				cache.Set("user", key, "user", int64(slot+1), int64(i+1))
				if i%8 == 0 {
					cache.Invalidate("user", key)
					cache.Set("user", key, "user", int64(slot+1), int64(i+2))
				}
			}
			b.StopTimer()

			cache.mu.RLock()
			entries := len(cache.entries)
			ordered := cache.order.Len()
			indexed := len(cache.orderIndex)
			cache.mu.RUnlock()
			if entries > maxEntries || ordered != entries || indexed != entries {
				b.Fatalf("peer cache entries/order/index=%d/%d/%d cap=%d", entries, ordered, indexed, maxEntries)
			}
			b.ReportMetric(float64(entries), "resident_entries")
			b.ReportMetric(float64(ordered), "ordering_nodes")
		})
	}
}
