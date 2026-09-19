package client

import (
	"strconv"
	"sync"
	"time"
)

// RateLimiter manages bucket-based rate limits per user and action category.
type RateLimiter interface {
	Allow(userID int64, category string) bool
}

type rateBucket struct {
	tokens     int
	lastRefill time.Time
	lastAccess time.Time
}

const maxUserRateLimiterBuckets = 4096

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

func (l *UserRateLimiter) reclaimIdleBucketsLocked(now time.Time) {
	for key, bucket := range l.buckets {
		missing := l.maxTokens - bucket.tokens
		if missing < 0 {
			missing = 0
		}
		fullyRefilledAt := bucket.lastRefill.Add(time.Duration(missing) * l.refillEvery)
		if now.Sub(bucket.lastAccess) >= time.Minute && !now.Before(fullyRefilledAt) {
			delete(l.buckets, key)
		}
	}
}

// Allow reports whether an action by userID under category is allowed within rate limits.
func (l *UserRateLimiter) Allow(userID int64, category string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	key := strconv.FormatInt(userID, 10) + ":" + category

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxUserRateLimiterBuckets {
			l.reclaimIdleBucketsLocked(now)
			if len(l.buckets) >= maxUserRateLimiterBuckets {
				// Cardinality pressure must not reset another active user's
				// limiter state. New identities are denied until idle state can
				// be reclaimed.
				return false
			}
		}
		l.buckets[key] = &rateBucket{
			tokens:     l.maxTokens - 1,
			lastRefill: now,
			lastAccess: now,
		}
		return true
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastRefill)
	refill := int(elapsed / l.refillEvery)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > l.maxTokens {
			b.tokens = l.maxTokens
		}
		b.lastRefill = now
	}
	b.lastAccess = now

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}
