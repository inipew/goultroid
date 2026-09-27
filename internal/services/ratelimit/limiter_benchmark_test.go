package ratelimit

import (
	"context"
	"fmt"
	"testing"
	"time"
)

var p3aLimiterResult Result

func BenchmarkLimiterHighCardinalityP3A(b *testing.B) {
	policy := Policy{Limit: 1_000_000_000, Window: time.Second, Burst: 1_000_000_000}
	for _, count := range []int{1, 16, 64, 256, 4096} {
		b.Run(fmt.Sprintf("hot/%d", count), func(b *testing.B) {
			l := New(policy, time.Minute)
			keys := make([]string, count)
			for i := range keys {
				keys[i] = fmt.Sprintf("user-%04d", i)
				if !l.Allow(DimensionUser, keys[i]) {
					b.Fatalf("seed key %d rejected", i)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p3aLimiterResult = l.Take(context.Background(), DimensionUser, keys[i%len(keys)], 1)
			}
		})

		b.Run(fmt.Sprintf("insert/%d", count), func(b *testing.B) {
			keys := make([]string, count)
			for i := range keys {
				keys[i] = fmt.Sprintf("user-%04d", i)
			}
			b.ReportAllocs()
			b.ReportMetric(float64(count), "keys/op")
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				l := New(policy, time.Minute)
				b.StartTimer()
				for _, key := range keys {
					p3aLimiterResult = l.Take(context.Background(), DimensionUser, key, 1)
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkLimiterSaturatedCapacityP3A(b *testing.B) {
	policy := Policy{Limit: 1_000_000, Window: time.Hour, Burst: 1_000_000}
	l := New(policy, time.Minute)
	for i := 0; i < defaultMaxBuckets; i++ {
		if !l.Allow(DimensionUser, fmt.Sprintf("seed-%04d", i)) {
			b.Fatalf("seed key %d rejected", i)
		}
	}
	l.lastCapacitySweep = time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p3aLimiterResult = l.Take(context.Background(), DimensionUser, "overflow", 1)
	}
}

func BenchmarkLimiterCapacitySweep4096P3A(b *testing.B) {
	policy := Policy{Limit: 1_000_000, Window: time.Hour, Burst: 1_000_000}
	b.ReportAllocs()
	b.ReportMetric(defaultMaxBuckets, "buckets/op")
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		l := New(policy, time.Minute)
		for key := 0; key < defaultMaxBuckets; key++ {
			composite := l.formatKey(DimensionUser, fmt.Sprintf("seed-%04d", key))
			l.buckets[composite] = &bucket{
				tokens:     float64(policy.Burst),
				lastRefill: time.Now(),
				policy:     policy,
				lastAccess: time.Now(),
			}
		}
		l.lastCapacitySweep = time.Time{}
		b.StartTimer()
		p3aLimiterResult = l.Take(context.Background(), DimensionUser, "overflow", 1)
		b.StopTimer()
	}
}
