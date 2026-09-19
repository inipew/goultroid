package ratelimit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

type bucket struct {
	tokens     float64
	lastRefill time.Time
	policy     Policy
	lastAccess time.Time
}

const (
	defaultMaxBuckets           = 4096
	capacitySweepInterval       = 30 * time.Second
)

// Limiter provides multi-dimensional token-bucket rate-limiting.
type Limiter struct {
	mu              sync.RWMutex
	buckets         map[string]*bucket
	policies        map[Dimension]Policy
	defaultPolicy   Policy
	cleanupInterval   time.Duration
	maxBuckets        int
	lastCapacitySweep time.Time

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	stopCh      chan struct{}
	doneCh      chan struct{}
}

// New creates a new Limiter with the given default policy and cleanup interval without starting background workers.
func New(defaultPolicy Policy, cleanupInterval time.Duration) *Limiter {
	if defaultPolicy.Window <= 0 {
		defaultPolicy.Window = time.Minute
	}
	if defaultPolicy.Limit <= 0 {
		defaultPolicy.Limit = 60
	}
	if defaultPolicy.Burst <= 0 {
		defaultPolicy.Burst = defaultPolicy.Limit
	}
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}

	return &Limiter{
		buckets:         make(map[string]*bucket),
		policies:        make(map[Dimension]Policy),
		defaultPolicy:   defaultPolicy,
		cleanupInterval: cleanupInterval,
		maxBuckets:      defaultMaxBuckets,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
}

// SetPolicy sets a specialized policy for a specific dimension.
func (l *Limiter) SetPolicy(dim Dimension, policy Policy) {
	if policy.Window <= 0 {
		policy.Window = time.Minute
	}
	if policy.Limit <= 0 {
		policy.Limit = 60
	}
	if policy.Burst <= 0 {
		policy.Burst = policy.Limit
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.policies[dim] = policy
}

// formatKey formats dimension and entity ID into a composite key.
func (l *Limiter) formatKey(dim Dimension, key string) string {
	return fmt.Sprintf("%s:%s", dim, key)
}

func (l *Limiter) getPolicy(dim Dimension) Policy {
	if p, ok := l.policies[dim]; ok {
		return p
	}
	return l.defaultPolicy
}

func bucketIdleExpired(now time.Time, b *bucket) bool {
	if b == nil {
		return true
	}
	maxAge := b.policy.Window * 2
	if maxAge < 5*time.Minute {
		maxAge = 5 * time.Minute
	}
	return now.Sub(b.lastAccess) > maxAge
}

func (l *Limiter) cleanupIdleBucketsLocked(now time.Time) {
	for key, b := range l.buckets {
		if bucketIdleExpired(now, b) {
			delete(l.buckets, key)
		}
	}
}

func (l *Limiter) ensureBucketCapacityLocked(now time.Time) bool {
	if l.maxBuckets <= 0 || len(l.buckets) < l.maxBuckets {
		return true
	}
	if l.lastCapacitySweep.IsZero() || now.Sub(l.lastCapacitySweep) >= capacitySweepInterval {
		l.cleanupIdleBucketsLocked(now)
		l.lastCapacitySweep = now
	}
	return len(l.buckets) < l.maxBuckets
}

// Allow reports whether a single token can be consumed immediately.
func (l *Limiter) Allow(dim Dimension, key string) bool {
	res := l.Take(context.Background(), dim, key, 1)
	return res.Allowed
}

// Take attempts to consume `cost` tokens from the bucket.
func (l *Limiter) Take(ctx context.Context, dim Dimension, key string, cost int) Result {
	if cost <= 0 {
		cost = 1
	}

	compositeKey := l.formatKey(dim, key)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[compositeKey]
	if !exists {
		p := l.getPolicy(dim)
		if !l.ensureBucketCapacityLocked(now) {
			retryAfter := l.cleanupInterval
			if retryAfter <= 0 {
				retryAfter = 5 * time.Minute
			}
			return Result{Allowed: false, RetryAfter: retryAfter, ResetAfter: retryAfter}
		}
		b = &bucket{
			tokens:     float64(p.Burst),
			lastRefill: now,
			policy:     p,
			lastAccess: now,
		}
		l.buckets[compositeKey] = b
	}

	// 1. Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		fillRate := float64(b.policy.Limit) / b.policy.Window.Seconds()
		b.tokens = math.Min(float64(b.policy.Burst), b.tokens+(elapsed.Seconds()*fillRate))
		b.lastRefill = now
	}
	b.lastAccess = now

	// 2. Check if enough tokens exist
	if b.tokens >= float64(cost) {
		b.tokens -= float64(cost)
		remaining := int(math.Floor(b.tokens))

		// Time to refill completely
		fillRate := float64(b.policy.Limit) / b.policy.Window.Seconds()
		var resetAfter time.Duration
		if fillRate > 0 {
			deficit := float64(b.policy.Burst) - b.tokens
			resetAfter = time.Duration((deficit / fillRate) * float64(time.Second))
		}

		return Result{
			Allowed:    true,
			Remaining:  remaining,
			RetryAfter: 0,
			ResetAfter: resetAfter,
		}
	}

	// Denied - compute retry after
	fillRate := float64(b.policy.Limit) / b.policy.Window.Seconds()
	var retryAfter time.Duration
	if fillRate > 0 {
		needed := float64(cost) - b.tokens
		retryAfter = time.Duration(math.Ceil(needed/fillRate) * float64(time.Second))
		if retryAfter < time.Millisecond {
			retryAfter = time.Millisecond
		}
	} else {
		retryAfter = b.policy.Window
	}

	return Result{
		Allowed:    false,
		Remaining:  int(math.Floor(b.tokens)),
		RetryAfter: retryAfter,
		ResetAfter: retryAfter,
	}
}

// Reset clears the bucket for the given dimension and key.
func (l *Limiter) Reset(dim Dimension, key string) {
	compositeKey := l.formatKey(dim, key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, compositeKey)
}

// Start initiates the background cleanup worker. It is safe to call repeatedly.
func (l *Limiter) Start(ctx context.Context) error {
	l.lifecycleMu.Lock()
	defer l.lifecycleMu.Unlock()
	if l.started || l.stopped {
		return nil
	}
	l.started = true
	go l.cleanupLoop(l.cleanupInterval)
	return nil
}

// Stop gracefully shuts down the background cleanup worker, waiting for termination up to ctx deadline.
func (l *Limiter) Stop(ctx context.Context) error {
	l.lifecycleMu.Lock()
	if !l.started {
		l.stopped = true
		l.lifecycleMu.Unlock()
		return nil
	}
	if l.stopped {
		l.lifecycleMu.Unlock()
		return nil
	}
	l.stopped = true
	close(l.stopCh)
	done := l.doneCh
	l.lifecycleMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// cleanupLoop periodically removes buckets that haven't been accessed in twice their window.
func (l *Limiter) cleanupLoop(interval time.Duration) {
	defer close(l.doneCh)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case now := <-ticker.C:
			l.mu.Lock()
			l.cleanupIdleBucketsLocked(now)
			l.mu.Unlock()
		}
	}
}

// Close gracefully stops the cleanup background worker for backward compatibility.
func (l *Limiter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return l.Stop(ctx)
}
