package telegram

import (
	"container/heap"
	"container/list"
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
	lastAccess time.Time
	lruElem    *list.Element
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

type penaltyState struct {
	key   LimitKey
	until time.Time
	index int
}

type penaltyHeap []*penaltyState

func (h penaltyHeap) Len() int { return len(h) }
func (h penaltyHeap) Less(i, j int) bool {
	return h[i].until.Before(h[j].until)
}
func (h penaltyHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *penaltyHeap) Push(value any) {
	state := value.(*penaltyState)
	state.index = len(*h)
	*h = append(*h, state)
}
func (h *penaltyHeap) Pop() any {
	old := *h
	n := len(old)
	state := old[n-1]
	old[n-1] = nil
	state.index = -1
	*h = old[:n-1]
	return state
}

// HierarchicalRPCLimiter provides atomic multi-dimensional rate limiting and penalty management.
//
// The hot path is intentionally O(number of request dimensions), not O(total
// limiter cardinality). Dynamic buckets are ordered by last access in an LRU
// list, so idle reclamation only inspects the oldest entries. Penalties use an
// indexed expiry heap, keeping one heap node per active penalty.
//
// When bounded state is saturated the limiter fails closed. It never evicts a
// live token bucket merely to admit a new identity, and it never drops an
// active FloodWait penalty. Penalty overflow is conservatively promoted to a
// temporary account-wide cooldown.
type HierarchicalRPCLimiter struct {
	mu  sync.Mutex
	cfg HierarchicalLimiterConfig

	buckets   map[LimitKey]*tokenBucket
	bucketLRU *list.List // non-global buckets, oldest access at Front

	penalties map[LimitKey]*penaltyState
	penaltyQ  penaltyHeap

	// overflowPenaltyUntil is used only when MaxPenalties is saturated by live
	// entries. Promoting the overflow to an account-wide cooldown is stricter
	// than dropping Telegram's FloodWait instruction and remains constant-space.
	overflowPenaltyUntil time.Time
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

	l := &HierarchicalRPCLimiter{
		cfg:       cfg,
		buckets:   make(map[LimitKey]*tokenBucket),
		bucketLRU: list.New(),
		penalties: make(map[LimitKey]*penaltyState),
	}
	heap.Init(&l.penaltyQ)
	return l
}

func (l *HierarchicalRPCLimiter) bucketConfig(key LimitKey) (rate, capacity float64) {
	switch key.Scope {
	case "global":
		rate = l.cfg.GlobalRate
		capacity = l.cfg.GlobalBurst
	case "family":
		if fcfg, ok := l.cfg.FamilyConfigs[key.Key]; ok {
			rate = fcfg.Rate
			capacity = fcfg.Capacity
		} else {
			rate = l.cfg.DefaultFamilyRate
			capacity = l.cfg.DefaultFamilyBurst
		}
	case "method":
		if mcfg, ok := l.cfg.MethodConfigs[key.Key]; ok {
			rate = mcfg.Rate
			capacity = mcfg.Capacity
		} else {
			rate = l.cfg.DefaultMethodRate
			capacity = l.cfg.DefaultMethodBurst
		}
	case "peer":
		rate = l.cfg.DefaultPeerRate
		capacity = l.cfg.DefaultPeerBurst
	default:
		rate = l.cfg.GlobalRate
		capacity = l.cfg.GlobalBurst
	}
	if rate <= 0 {
		rate = 1.0
	}
	if capacity <= 0 {
		capacity = rate
	}
	return rate, capacity
}

func (l *HierarchicalRPCLimiter) touchBucketLocked(key LimitKey, bucket *tokenBucket, now time.Time) {
	bucket.lastAccess = now
	if key.Scope == "global" {
		return
	}
	if bucket.lruElem == nil {
		bucket.lruElem = l.bucketLRU.PushBack(key)
		return
	}
	l.bucketLRU.MoveToBack(bucket.lruElem)
}

func (l *HierarchicalRPCLimiter) getOrCreateBucketLocked(key LimitKey, now time.Time) *tokenBucket {
	if bucket, ok := l.buckets[key]; ok {
		l.touchBucketLocked(key, bucket, now)
		return bucket
	}
	if len(l.buckets) >= l.cfg.MaxBuckets {
		return nil
	}

	rate, capacity := l.bucketConfig(key)
	bucket := &tokenBucket{
		rate:       rate,
		capacity:   capacity,
		tokens:     capacity,
		lastRefill: now,
		lastAccess: now,
	}
	l.buckets[key] = bucket
	l.touchBucketLocked(key, bucket, now)
	return bucket
}

func (l *HierarchicalRPCLimiter) removeBucketLocked(key LimitKey, bucket *tokenBucket) {
	if bucket != nil && bucket.lruElem != nil {
		l.bucketLRU.Remove(bucket.lruElem)
		bucket.lruElem = nil
	}
	delete(l.buckets, key)
}

// cleanupIdleBucketsLocked is amortized O(number of entries actually expired).
// Because the LRU list is ordered by last access, once the oldest entry is not
// expired no later entry can be expired either.
func (l *HierarchicalRPCLimiter) cleanupIdleBucketsLocked(now time.Time) {
	for {
		elem := l.bucketLRU.Front()
		if elem == nil {
			return
		}
		key := elem.Value.(LimitKey)
		bucket := l.buckets[key]
		if bucket == nil {
			l.bucketLRU.Remove(elem)
			continue
		}
		if now.Sub(bucket.lastAccess) <= l.cfg.IdleTTL {
			return
		}
		l.removeBucketLocked(key, bucket)
	}
}

func (l *HierarchicalRPCLimiter) bucketCapacityRetryAfterLocked(now time.Time) time.Duration {
	elem := l.bucketLRU.Front()
	if elem == nil {
		return l.cfg.IdleTTL
	}
	key := elem.Value.(LimitKey)
	bucket := l.buckets[key]
	if bucket == nil {
		return time.Millisecond
	}
	retryAfter := bucket.lastAccess.Add(l.cfg.IdleTTL).Sub(now)
	if retryAfter <= 0 {
		return time.Millisecond
	}
	return retryAfter
}

func (l *HierarchicalRPCLimiter) removePenaltyLocked(state *penaltyState) {
	if state == nil {
		return
	}
	if state.index >= 0 && state.index < l.penaltyQ.Len() {
		heap.Remove(&l.penaltyQ, state.index)
	}
	delete(l.penalties, state.key)
}

func (l *HierarchicalRPCLimiter) cleanupExpiredPenaltiesLocked(now time.Time) {
	for l.penaltyQ.Len() > 0 {
		state := l.penaltyQ[0]
		if state.until.After(now) {
			return
		}
		heap.Pop(&l.penaltyQ)
		delete(l.penalties, state.key)
	}
	if !l.overflowPenaltyUntil.IsZero() && !l.overflowPenaltyUntil.After(now) {
		l.overflowPenaltyUntil = time.Time{}
	}
}

func (l *HierarchicalRPCLimiter) missingBucketCountLocked(dimensions []LimitKey) int {
	seen := make(map[LimitKey]struct{}, len(dimensions))
	missing := 0
	for _, dim := range dimensions {
		if _, ok := l.buckets[dim]; ok {
			continue
		}
		if _, duplicate := seen[dim]; duplicate {
			continue
		}
		seen[dim] = struct{}{}
		missing++
	}
	return missing
}

// Reserve checks rate limits and FloodWait penalties across all dimensions atomically.
func (l *HierarchicalRPCLimiter) Reserve(now time.Time, dimensions []LimitKey, cost int) Reservation {
	if cost <= 0 {
		cost = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanupExpiredPenaltiesLocked(now)
	if l.overflowPenaltyUntil.After(now) {
		return Reservation{Allowed: false, RetryAfter: l.overflowPenaltyUntil.Sub(now)}
	}

	// Check only penalties associated with this request. This is O(dimensions)
	// regardless of the number of other peers currently represented.
	var maxPenaltyWait time.Duration
	for _, dim := range dimensions {
		state, ok := l.penalties[dim]
		if !ok {
			continue
		}
		if !state.until.After(now) {
			l.removePenaltyLocked(state)
			continue
		}
		wait := state.until.Sub(now)
		if wait > maxPenaltyWait {
			maxPenaltyWait = wait
		}
	}
	if maxPenaltyWait > 0 {
		return Reservation{Allowed: false, RetryAfter: maxPenaltyWait}
	}

	// Reclaim only LRU entries that are actually idle. A depleted bucket is
	// still reclaimable after IdleTTL; token fullness is not an activity signal.
	l.cleanupIdleBucketsLocked(now)

	// Reserve state capacity atomically before creating any new dimensions.
	// Saturation is a fail-closed admission result, never a reason to discard a
	// live bucket and reset its rate-limit history.
	if missing := l.missingBucketCountLocked(dimensions); len(l.buckets)+missing > l.cfg.MaxBuckets {
		return Reservation{Allowed: false, RetryAfter: l.bucketCapacityRetryAfterLocked(now)}
	}

	reqCost := float64(cost)
	var maxWait time.Duration
	matchedBuckets := make([]*tokenBucket, 0, len(dimensions))

	for _, dim := range dimensions {
		bucket := l.getOrCreateBucketLocked(dim, now)
		if bucket == nil {
			// Defensive fallback for impossible capacity races while holding l.mu.
			return Reservation{Allowed: false, RetryAfter: l.bucketCapacityRetryAfterLocked(now)}
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

	// If any dimension lacks tokens, reject atomically without deducting from any
	// other dimension. Access timestamps are intentionally refreshed: repeated
	// attempts are activity and must not reset a hot bucket through idle eviction.
	if maxWait > 0 {
		return Reservation{Allowed: false, RetryAfter: maxWait}
	}

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
		if state, ok := l.penalties[dim]; ok {
			if until.After(state.until) {
				state.until = until
				heap.Fix(&l.penaltyQ, state.index)
			}
			continue
		}

		if len(l.penalties) >= l.cfg.MaxPenalties {
			// Dropping an active Telegram penalty would fail open. Preserve a
			// bounded representation by promoting overflow to account-wide
			// cooldown until the new instruction expires.
			if until.After(l.overflowPenaltyUntil) {
				l.overflowPenaltyUntil = until
			}
			continue
		}

		state := &penaltyState{key: dim, until: until, index: -1}
		l.penalties[dim] = state
		heap.Push(&l.penaltyQ, state)
	}
}

// Size returns bounded internal state counts for diagnostics and tests.
func (l *HierarchicalRPCLimiter) Size() (buckets, penalties int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets), len(l.penalties)
}
