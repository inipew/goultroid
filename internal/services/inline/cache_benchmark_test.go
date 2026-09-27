package inline

import (
	"fmt"
	"testing"
	"time"
)

var (
	p3aCacheResults []InlineResult
	p3aCacheOK      bool
	p3aCacheExpiry  time.Time
)

func p3aCacheKeys(count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("inline:%04d", i)
	}
	return keys
}

func p3aCacheFixture() []InlineResult {
	return []InlineResult{{
		ID:          "result-1",
		Title:       "Representative inline result",
		Description: "P3-A cache benchmark fixture",
		Text:        "bounded payload",
	}}
}

func BenchmarkCacheHighCardinalityP3A(b *testing.B) {
	fixture := p3aCacheFixture()
	for _, count := range []int{1, 16, 64, 256, maxInlineCacheEntries} {
		b.Run(fmt.Sprintf("hit/%d", count), func(b *testing.B) {
			cache := NewCache(time.Hour)
			keys := p3aCacheKeys(count)
			for _, key := range keys {
				cache.SetScoped(key, fixture, time.Hour)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p3aCacheResults, p3aCacheOK = cache.GetScoped(keys[i%len(keys)])
			}
		})

		b.Run(fmt.Sprintf("fill/%d", count), func(b *testing.B) {
			keys := p3aCacheKeys(count)
			b.ReportAllocs()
			b.ReportMetric(float64(count), "entries/op")
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				cache := NewCache(time.Hour)
				b.StartTimer()
				for _, key := range keys {
					cache.SetScoped(key, fixture, time.Hour)
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkCacheSaturatedChurnP3A(b *testing.B) {
	fixture := p3aCacheFixture()
	for _, workingSet := range []int{maxInlineCacheEntries + 1, 4096} {
		b.Run(fmt.Sprintf("working-set/%d", workingSet), func(b *testing.B) {
			cache := NewCache(time.Hour)
			keys := p3aCacheKeys(workingSet)
			for i := 0; i < maxInlineCacheEntries; i++ {
				cache.SetScoped(keys[i], fixture, time.Hour)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				key := keys[(maxInlineCacheEntries+i)%workingSet]
				cache.SetScoped(key, fixture, time.Hour)
			}
		})
	}
}

func BenchmarkCacheNextExpiry500P3A(b *testing.B) {
	cache := NewCache(time.Hour)
	fixture := p3aCacheFixture()
	for _, key := range p3aCacheKeys(maxInlineCacheEntries) {
		cache.SetScoped(key, fixture, time.Hour)
	}

	b.ReportAllocs()
	b.ReportMetric(maxInlineCacheEntries, "entries/op")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p3aCacheExpiry, p3aCacheOK = cache.nextExpiry()
	}
}
