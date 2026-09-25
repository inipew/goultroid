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
	tokens      int
	capacity    int
	refillEvery time.Duration
	category    string
	lastRefill  time.Time
	lastAccess  time.Time
}

type rateLimitProfile struct {
	maxTokens   int
	refillEvery time.Duration
}

const (
	maxUserRateLimiterBuckets          = 4096
	userRateLimitCapacitySweepInterval = 30 * time.Second
)

// UserRateLimiter implements token bucket rate limiting.
type UserRateLimiter struct {
	mu                sync.Mutex
	buckets           map[string]*rateBucket
	maxTokens         int
	refillEvery       time.Duration
	profiles          map[string]rateLimitProfile
	lastCapacitySweep time.Time
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
		profiles:    make(map[string]rateLimitProfile),
	}
}

// SetCategoryLimit configures a bounded category-specific burst/refill profile.
// It is safe to call before or during runtime; existing buckets keep their
// accumulated tokens but are clamped to the new capacity.
func (l *UserRateLimiter) SetCategoryLimit(category string, maxTokens int, refillEvery time.Duration) {
	if l == nil || category == "" {
		return
	}
	if maxTokens <= 0 {
		maxTokens = l.maxTokens
	}
	if refillEvery <= 0 {
		refillEvery = l.refillEvery
	}
	l.mu.Lock()
	l.profiles[category] = rateLimitProfile{maxTokens: maxTokens, refillEvery: refillEvery}
	for _, bucket := range l.buckets {
		if bucket.category != category {
			continue
		}
		bucket.capacity = maxTokens
		bucket.refillEvery = refillEvery
		if bucket.tokens > maxTokens {
			bucket.tokens = maxTokens
		}
	}
	l.mu.Unlock()
}

func (l *UserRateLimiter) profileLocked(category string) rateLimitProfile {
	if profile, ok := l.profiles[category]; ok {
		return profile
	}
	return rateLimitProfile{maxTokens: l.maxTokens, refillEvery: l.refillEvery}
}

func (l *UserRateLimiter) reclaimIdleBucketsLocked(now time.Time) {
	for key, bucket := range l.buckets {
		missing := bucket.capacity - bucket.tokens
		if missing < 0 {
			missing = 0
		}
		fullyRefilledAt := bucket.lastRefill.Add(time.Duration(missing) * bucket.refillEvery)
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
	profile := l.profileLocked(category)

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxUserRateLimiterBuckets {
			if l.lastCapacitySweep.IsZero() || now.Sub(l.lastCapacitySweep) >= userRateLimitCapacitySweepInterval {
				l.reclaimIdleBucketsLocked(now)
				l.lastCapacitySweep = now
			}
			if len(l.buckets) >= maxUserRateLimiterBuckets {
				// Cardinality pressure must not reset another active user's
				// limiter state. New identities are denied until idle state can
				// be reclaimed.
				return false
			}
		}
		l.buckets[key] = &rateBucket{
			tokens:      profile.maxTokens - 1,
			capacity:    profile.maxTokens,
			refillEvery: profile.refillEvery,
			category:    category,
			lastRefill:  now,
			lastAccess:  now,
		}
		return true
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastRefill)
	refill := int(elapsed / b.refillEvery)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > b.capacity {
			b.tokens = b.capacity
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
