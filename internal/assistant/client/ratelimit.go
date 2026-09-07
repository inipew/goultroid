package client

import (
	"sync"
	"time"
)

// RateLimiter manages bucket-based rate limits per user and action category.
type RateLimiter interface {
	Allow(userID int64, category string) bool
}

type rateBucket struct {
	tokens int
	last   time.Time
}

// UserRateLimiter implements token bucket rate limiting.
type UserRateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*rateBucket
	maxTokens   int
	refillEvery time.Duration
}

// NewUserRateLimiter creates a RateLimiter with configured burst and refill rate.
func NewUserRateLimiter(maxTokens int, refillEvery time.Duration) *UserRateLimiter {
	if maxTokens <= 0 {
		maxTokens = 5
	}
	if refillEvery <= 0 {
		refillEvery = 2 * time.Second
	}
	return &UserRateLimiter{
		buckets:     make(map[string]*rateBucket),
		maxTokens:   maxTokens,
		refillEvery: refillEvery,
	}
}

// Allow reports whether an action by userID under category is allowed within rate limits.
func (l *UserRateLimiter) Allow(userID int64, category string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := string(rune(userID)) + ":" + category

	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &rateBucket{
			tokens: l.maxTokens - 1,
			last:   now,
		}

		// Periodic cleanup if map grows large
		if len(l.buckets) > 1000 {
			for k, bucket := range l.buckets {
				if now.Sub(bucket.last) > 1*time.Minute {
					delete(l.buckets, k)
				}
			}
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.last)
	refill := int(elapsed / l.refillEvery)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > l.maxTokens {
			b.tokens = l.maxTokens
		}
		b.last = now
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}
