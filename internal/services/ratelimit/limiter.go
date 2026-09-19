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
	defaultMaxBuckets     = 4096
	capacitySweepInterval = 30 * time.Second
)

// Limiter provides multi-dimensional token-bucket rate-limiting.
type Limiter struct {
	mu                sync.RWMutex
	buckets           map[string]*bucket
	policies          map[Dimension]Policy
	defaultPolicy     Policy
	cleanupInterval   time.Duration
	maxBuckets        int
	lastCapacitySweep time.Time

	lifecycleMu   sync.Mutex
	started       bool
	stopped       bool
	runCtx        context.Context
	cancel        context.CancelFunc
	wake          chan struct{}
	workerRunning bool
	workerDone    chan struct{}
}

// New creates a new Limiter without starting background workers.
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
		wake:            make(chan struct{}, 1),
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

func bucketIdleExpiry(b *bucket) time.Time {
	if b == nil {
		return time.Time{}
	}
	maxAge := b.policy.Window * 2
	if maxAge < 5*time.Minute {
		maxAge = 5 * time.Minute
	}
	return b.lastAccess.Add(maxAge)
}

func bucketIdleExpired(now time.Time, b *bucket) bool {
	expiry := bucketIdleExpiry(b)
	return expiry.IsZero() || !now.Before(expiry)
}

func (l *Limiter) cleanupIdleBucketsLocked(now time.Time) {
	for key, b := range l.buckets {
		if bucketIdleExpired(now, b) {
			delete(l.buckets, key)
		}
	}
}

func (l *Limiter) nextBucketExpiryLocked() (time.Time, bool) {
	var next time.Time
	for _, b := range l.buckets {
		expiry := bucketIdleExpiry(b)
		if expiry.IsZero() {
			continue
		}
		if next.IsZero() || expiry.Before(next) {
			next = expiry
		}
	}
	return next, !next.IsZero()
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

func (l *Limiter) ensureCleanupWorker() {
	if l == nil {
		return
	}
	l.lifecycleMu.Lock()
	if !l.started || l.stopped || l.runCtx == nil || l.runCtx.Err() != nil {
		l.lifecycleMu.Unlock()
		return
	}
	if !l.workerRunning {
		done := make(chan struct{})
		l.workerRunning = true
		l.workerDone = done
		runCtx := l.runCtx
		wake := l.wake
		go l.cleanupLoop(runCtx, wake, done)
	}
	wake := l.wake
	l.lifecycleMu.Unlock()

	select {
	case wake <- struct{}{}:
	default:
	}
}

func (l *Limiter) wakeCleanupWorker() {
	if l == nil {
		return
	}
	l.lifecycleMu.Lock()
	running := l.workerRunning
	wake := l.wake
	l.lifecycleMu.Unlock()
	if !running || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

// Allow reports whether a single token can be consumed immediately.
func (l *Limiter) Allow(dim Dimension, key string) bool {
	res := l.Take(context.Background(), dim, key, 1)
	return res.Allowed
}

// Take attempts to consume cost tokens from the bucket.
func (l *Limiter) Take(ctx context.Context, dim Dimension, key string, cost int) Result {
	if cost <= 0 {
		cost = 1
	}

	compositeKey := l.formatKey(dim, key)
	now := time.Now()
	created := false

	l.mu.Lock()
	b, exists := l.buckets[compositeKey]
	if !exists {
		p := l.getPolicy(dim)
		if !l.ensureBucketCapacityLocked(now) {
			retryAfter := l.cleanupInterval
			if retryAfter <= 0 {
				retryAfter = 5 * time.Minute
			}
			l.mu.Unlock()
			return Result{Allowed: false, RetryAfter: retryAfter, ResetAfter: retryAfter}
		}
		b = &bucket{
			tokens:     float64(p.Burst),
			lastRefill: now,
			policy:     p,
			lastAccess: now,
		}
		l.buckets[compositeKey] = b
		created = true
	}

	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		fillRate := float64(b.policy.Limit) / b.policy.Window.Seconds()
		b.tokens = math.Min(float64(b.policy.Burst), b.tokens+(elapsed.Seconds()*fillRate))
		b.lastRefill = now
	}
	b.lastAccess = now

	var result Result
	if b.tokens >= float64(cost) {
		b.tokens -= float64(cost)
		remaining := int(math.Floor(b.tokens))
		fillRate := float64(b.policy.Limit) / b.policy.Window.Seconds()
		var resetAfter time.Duration
		if fillRate > 0 {
			deficit := float64(b.policy.Burst) - b.tokens
			resetAfter = time.Duration((deficit / fillRate) * float64(time.Second))
		}
		result = Result{Allowed: true, Remaining: remaining, ResetAfter: resetAfter}
	} else {
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
		result = Result{
			Allowed:    false,
			Remaining:  int(math.Floor(b.tokens)),
			RetryAfter: retryAfter,
			ResetAfter: retryAfter,
		}
	}
	l.mu.Unlock()

	if created {
		l.ensureCleanupWorker()
	}
	return result
}

// Reset clears the bucket for the given dimension and key.
func (l *Limiter) Reset(dim Dimension, key string) {
	compositeKey := l.formatKey(dim, key)
	l.mu.Lock()
	_, existed := l.buckets[compositeKey]
	delete(l.buckets, compositeKey)
	l.mu.Unlock()
	if existed {
		l.wakeCleanupWorker()
	}
}

// Start activates lifecycle ownership without creating an idle cleanup
// goroutine. A deadline coordinator is started lazily when a bucket exists.
func (l *Limiter) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.lifecycleMu.Lock()
	if l.started || l.stopped {
		l.lifecycleMu.Unlock()
		return nil
	}
	l.runCtx, l.cancel = context.WithCancel(ctx)
	l.started = true
	if l.wake == nil {
		l.wake = make(chan struct{}, 1)
	}
	l.lifecycleMu.Unlock()

	l.mu.RLock()
	hasBuckets := len(l.buckets) > 0
	l.mu.RUnlock()
	if hasBuckets {
		l.ensureCleanupWorker()
	}
	return nil
}

// Stop cancels the lifecycle and joins the cleanup worker only when one exists.
func (l *Limiter) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.lifecycleMu.Lock()
	if l.stopped {
		l.lifecycleMu.Unlock()
		return nil
	}
	l.stopped = true
	cancel := l.cancel
	done := l.workerDone
	l.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) cleanupLoop(ctx context.Context, wake <-chan struct{}, done chan struct{}) {
	defer func() {
		l.lifecycleMu.Lock()
		if l.workerDone == done {
			l.workerRunning = false
			l.workerDone = nil
		}
		l.lifecycleMu.Unlock()
		close(done)
	}()

	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		l.mu.RLock()
		next, found := l.nextBucketExpiryLocked()
		l.mu.RUnlock()
		if !found {
			// Retirement is coordinated with insertion. If a new bucket arrives
			// before workerRunning is cleared, its ensure call either leaves a
			// wake behind or observes the retired state and starts a successor.
			l.lifecycleMu.Lock()
			if l.workerDone != done {
				l.lifecycleMu.Unlock()
				return
			}
			l.mu.RLock()
			hasBuckets := len(l.buckets) > 0
			l.mu.RUnlock()
			if !hasBuckets {
				l.workerRunning = false
				l.workerDone = nil
				l.lifecycleMu.Unlock()
				return
			}
			l.lifecycleMu.Unlock()
			continue
		}

		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(wait)
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
			continue
		case now := <-timer.C:
			l.mu.Lock()
			l.cleanupIdleBucketsLocked(now)
			l.mu.Unlock()
		}
	}
}

// Close gracefully stops cleanup lifecycle for backward compatibility.
func (l *Limiter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return l.Stop(ctx)
}
