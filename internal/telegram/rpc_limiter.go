package telegram

import (
	"math"
	"sync"
	"time"
)

// LimiterBucketConfig configures a token bucket's replenishment rate and burst capacity.
type LimiterBucketConfig struct {
	Rate     float64 // tokens per second
	Capacity float64 // maximum burst tokens
}

const (
	DefaultMaxLimiterBuckets   = 4096
	DefaultMaxLimiterPenalties = 4096
	MaxPenaltyDuration         = 24 * time.Hour
)

// HierarchicalLimiterConfig holds configuration for all rate-limiting tiers.
type HierarchicalLimiterConfig struct {
	GlobalRate         float64
	GlobalBurst        float64
	FamilyConfigs      map[string]LimiterBucketConfig
	DefaultFamilyRate  float64
	DefaultFamilyBurst float64
	MethodConfigs      map[string]LimiterBucketConfig
	DefaultMethodRate  float64
	DefaultMethodBurst float64
	DefaultPeerRate    float64
	DefaultPeerBurst   float64
	MaxBuckets         int
	MaxPenalties       int
	IdleTTL            time.Duration
}

// DefaultHierarchicalLimiterConfig returns standard production limits for Telegram MTProto.
func DefaultHierarchicalLimiterConfig() HierarchicalLimiterConfig {
	return HierarchicalLimiterConfig{
		GlobalRate:         30.0,
		GlobalBurst:        30.0,
		DefaultFamilyRate:  15.0,
		DefaultFamilyBurst: 20.0,
		FamilyConfigs: map[string]LimiterBucketConfig{
			"messages": {Rate: 5.0, Capacity: 10.0},
			"contacts": {Rate: 10.0, Capacity: 20.0},
			"channels": {Rate: 10.0, Capacity: 20.0},
			"photos":   {Rate: 10.0, Capacity: 15.0},
			"updates":  {Rate: 20.0, Capacity: 30.0},
		},
		MethodConfigs:      make(map[string]LimiterBucketConfig),
		DefaultMethodRate:  15.0,
		DefaultMethodBurst: 20.0,
		DefaultPeerRate:    1.0,
		DefaultPeerBurst:   3.0,
		MaxBuckets:         DefaultMaxLimiterBuckets,
		MaxPenalties:       DefaultMaxLimiterPenalties,
		IdleTTL:            10 * time.Minute,
	}
}

type tokenBucket struct {
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
}

func (b *tokenBucket) refill(now time.Time) {
	if b.lastRefill.IsZero() {
		b.lastRefill = now
		b.tokens = b.capacity
		return
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastRefill = now
}

// HierarchicalRPCLimiter provides atomic multi-dimensional rate limiting and penalty management.
type HierarchicalRPCLimiter struct {
	mu        sync.Mutex
	cfg       HierarchicalLimiterConfig
	buckets   map[LimitKey]*tokenBucket
	penalties map[LimitKey]time.Time
}

// NewHierarchicalRPCLimiter creates a new limiter with the specified config.
func NewHierarchicalRPCLimiter(cfg HierarchicalLimiterConfig) *HierarchicalRPCLimiter {
	if cfg.GlobalRate <= 0 {
		cfg.GlobalRate = 30.0
	}
	if cfg.GlobalBurst <= 0 {
		cfg.GlobalBurst = 30.0
	}
	if cfg.DefaultFamilyRate <= 0 {
		cfg.DefaultFamilyRate = 15.0
	}
	if cfg.DefaultFamilyBurst <= 0 {
		cfg.DefaultFamilyBurst = 20.0
	}
	if cfg.DefaultMethodRate <= 0 {
		cfg.DefaultMethodRate = 15.0
	}
	if cfg.DefaultMethodBurst <= 0 {
		cfg.DefaultMethodBurst = 20.0
	}
	if cfg.DefaultPeerRate <= 0 {
		cfg.DefaultPeerRate = 1.0
	}
	if cfg.DefaultPeerBurst <= 0 {
		cfg.DefaultPeerBurst = 3.0
	}
	if cfg.MaxBuckets <= 0 {
		cfg.MaxBuckets = DefaultMaxLimiterBuckets
	}
	if cfg.MaxPenalties <= 0 {
		cfg.MaxPenalties = DefaultMaxLimiterPenalties
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}
	if cfg.FamilyConfigs == nil {
		cfg.FamilyConfigs = make(map[string]LimiterBucketConfig)
	}
	if cfg.MethodConfigs == nil {
		cfg.MethodConfigs = make(map[string]LimiterBucketConfig)
	}

	return &HierarchicalRPCLimiter{
		cfg:       cfg,
		buckets:   make(map[LimitKey]*tokenBucket),
		penalties: make(map[LimitKey]time.Time),
	}
}

func (l *HierarchicalRPCLimiter) getOrCreateBucketLocked(key LimitKey, now time.Time) *tokenBucket {
	if b, ok := l.buckets[key]; ok {
		return b
	}

	var rate, cap float64
	switch key.Scope {
	case "global":
		rate = l.cfg.GlobalRate
		cap = l.cfg.GlobalBurst
	case "family":
		if fcfg, ok := l.cfg.FamilyConfigs[key.Key]; ok {
			rate = fcfg.Rate
			cap = fcfg.Capacity
		} else {
			rate = l.cfg.DefaultFamilyRate
			cap = l.cfg.DefaultFamilyBurst
		}
	case "method":
		if mcfg, ok := l.cfg.MethodConfigs[key.Key]; ok {
			rate = mcfg.Rate
			cap = mcfg.Capacity
		} else {
			rate = l.cfg.DefaultMethodRate
			cap = l.cfg.DefaultMethodBurst
		}
	case "peer":
		rate = l.cfg.DefaultPeerRate
		cap = l.cfg.DefaultPeerBurst
	default:
		rate = l.cfg.GlobalRate
		cap = l.cfg.GlobalBurst
	}

	if rate <= 0 {
		rate = 1.0
	}
	if cap <= 0 {
		cap = rate
	}
	if len(l.buckets) >= l.cfg.MaxBuckets {
		l.evictOldestDynamicBucketLocked()
		if len(l.buckets) >= l.cfg.MaxBuckets {
			return nil
		}
	}

	b := &tokenBucket{
		rate:       rate,
		capacity:   cap,
		tokens:     cap,
		lastRefill: now,
	}
	l.buckets[key] = b
	return b
}

// Reserve checks rate limits and FloodWait penalties across all dimensions atomically.
func (l *HierarchicalRPCLimiter) Reserve(now time.Time, dimensions []LimitKey, cost int) Reservation {
	if cost <= 0 {
		cost = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. Check FloodWait penalties on any associated dimension
	var maxPenaltyWait time.Duration
	for _, dim := range dimensions {
		if until, ok := l.penalties[dim]; ok {
			if until.After(now) {
				wait := until.Sub(now)
				if wait > maxPenaltyWait {
					maxPenaltyWait = wait
				}
			} else {
				delete(l.penalties, dim)
			}
		}
	}
	if maxPenaltyWait > 0 {
		return Reservation{
			Allowed:    false,
			RetryAfter: maxPenaltyWait,
		}
	}

	// 2. Expire idle dynamic state before evaluating capacity.
	l.cleanupIdleBucketsLocked(now)
	l.cleanupExpiredPenaltiesLocked(now)

	// 3. Check token availability for each active dimension
	reqCost := float64(cost)
	var maxWait time.Duration
	var matchedBuckets []*tokenBucket

	for _, dim := range dimensions {
		bucket := l.getOrCreateBucketLocked(dim, now)
		if bucket == nil {
			continue
		}
		bucket.refill(now)
		matchedBuckets = append(matchedBuckets, bucket)

		if bucket.tokens < reqCost {
			deficit := reqCost - bucket.tokens
			waitSec := deficit / bucket.rate
			waitDur := time.Duration(math.Ceil(waitSec * float64(time.Second)))
			if waitDur < time.Millisecond {
				waitDur = time.Millisecond
			}
			if waitDur > maxWait {
				maxWait = waitDur
			}
		}
	}

	// If any dimension doesn't have sufficient tokens, atomically reject without deducting any tokens
	if maxWait > 0 {
		return Reservation{
			Allowed:    false,
			RetryAfter: maxWait,
		}
	}

	// Deduct tokens from all checked dimensions atomically
	for _, bucket := range matchedBuckets {
		bucket.tokens -= reqCost
	}

	return Reservation{Allowed: true}
}

// Penalize registers a FloodWait backoff penalty across the specified dimensions.
func (l *HierarchicalRPCLimiter) Penalize(now time.Time, dimensions []LimitKey, retryAfter time.Duration) {
	if retryAfter <= 0 {
		return
	}
	if retryAfter > MaxPenaltyDuration {
		retryAfter = MaxPenaltyDuration
	}
	until := now.Add(retryAfter)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupExpiredPenaltiesLocked(now)

	for _, dim := range dimensions {
		if _, exists := l.penalties[dim]; !exists && len(l.penalties) >= l.cfg.MaxPenalties {
			l.evictEarliestPenaltyLocked()
		}
		if current, ok := l.penalties[dim]; !ok || until.After(current) {
			l.penalties[dim] = until
		}
	}
}

func (l *HierarchicalRPCLimiter) cleanupIdleBucketsLocked(now time.Time) {
	for key, b := range l.buckets {
		if key.Scope != "global" && b.tokens >= b.capacity && now.Sub(b.lastRefill) > l.cfg.IdleTTL {
			delete(l.buckets, key)
		}
	}
}

func (l *HierarchicalRPCLimiter) cleanupExpiredPenaltiesLocked(now time.Time) {
	for key, until := range l.penalties {
		if !until.After(now) {
			delete(l.penalties, key)
		}
	}
}

func (l *HierarchicalRPCLimiter) evictOldestDynamicBucketLocked() {
	var oldestKey LimitKey
	var oldest time.Time
	found := false
	for key, bucket := range l.buckets {
		if key.Scope == "global" {
			continue
		}
		if !found || bucket.lastRefill.Before(oldest) {
			oldestKey, oldest, found = key, bucket.lastRefill, true
		}
	}
	if found {
		delete(l.buckets, oldestKey)
	}
}

func (l *HierarchicalRPCLimiter) evictEarliestPenaltyLocked() {
	var earliestKey LimitKey
	var earliest time.Time
	found := false
	for key, until := range l.penalties {
		if !found || until.Before(earliest) {
			earliestKey, earliest, found = key, until, true
		}
	}
	if found {
		delete(l.penalties, earliestKey)
	}
}

// Size returns bounded internal state counts for diagnostics and tests.
func (l *HierarchicalRPCLimiter) Size() (buckets, penalties int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets), len(l.penalties)
}
