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

// Limiter provides multi-dimensional token-bucket rate-limiting.
type Limiter struct {
	mu            sync.RWMutex
	buckets       map[string]*bucket
	policies      map[Dimension]Policy
	defaultPolicy Policy

	stopOnce sync.Once
	stopCh   chan struct{}
}

// New creates a new Limiter with the given default policy and cleanup interval.
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

	l := &Limiter{
		buckets:       make(map[string]*bucket),
		policies:      make(map[Dimension]Policy),
		defaultPolicy: defaultPolicy,
		stopCh:        make(chan struct{}),
	}

	go l.cleanupLoop(cleanupInterval)
	return l
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

// cleanupLoop periodically removes buckets that haven't been accessed in twice their window.
func (l *Limiter) cleanupLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case now := <-ticker.C:
			l.mu.Lock()
			for k, b := range l.buckets {
				maxAge := b.policy.Window * 2
				if maxAge < 5*time.Minute {
					maxAge = 5 * time.Minute
				}
				if now.Sub(b.lastAccess) > maxAge {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Close gracefully stops the cleanup background worker.
func (l *Limiter) Close() error {
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
	return nil
}
