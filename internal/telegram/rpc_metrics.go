package telegram

import (
	"sync"
	"sync/atomic"
	"time"
)

// RPCMetrics defines metrics collection for outbound Telegram RPC calls.
// Labels must avoid high-cardinality values (e.g. user/chat IDs, usernames, raw error strings).
type RPCMetrics interface {
	ObserveRequest(method string, class RPCErrorClass, attempt int, elapsed time.Duration)
	ObserveWait(scope string, elapsed time.Duration)
	ObserveFloodWait(method string, retryAfter time.Duration, deferred bool)
}

// NoopRPCMetrics implements RPCMetrics by discarding all observations.
type NoopRPCMetrics struct{}

func (NoopRPCMetrics) ObserveRequest(string, RPCErrorClass, int, time.Duration) {}
func (NoopRPCMetrics) ObserveWait(string, time.Duration)                        {}
func (NoopRPCMetrics) ObserveFloodWait(string, time.Duration, bool)             {}

// RPCMetricsSnapshot holds a copy of aggregated metrics for diagnostics.
type RPCMetricsSnapshot struct {
	TotalRequests     int64
	RequestsByClass   map[RPCErrorClass]int64
	RequestsByMethod  map[string]int64
	TotalWaitTime     time.Duration
	WaitTimeByScope   map[string]time.Duration
	FloodWaitCount    int64
	FloodWaitDeferred int64
	FloodWaitTotal    time.Duration
}

const (
	rpcErrorClassCount       = int(RPCInvalidRequest) + 1
	maxRPCMethodMetricLabels = 512
	maxRPCWaitMetricLabels   = 32
	rpcMetricOverflowLabel   = "__other__"
)

type rpcMethodCounters struct {
	requests atomic.Int64
}

type rpcWaitCounters struct {
	nanos atomic.Int64
}

// InMemoryRPCMetrics tracks RPC metrics with a lock-free steady hot path.
//
// Error classes are fixed-cardinality atomics. Method and wait-scope labels are
// registered lazily in sync.Map; after first observation the hot path is a map
// load plus per-label atomic increments. Snapshot pays the aggregation cost
// instead of serializing every physical Telegram RPC on one global mutex.
type InMemoryRPCMetrics struct {
	requestsByClass   [rpcErrorClassCount]atomic.Int64
	requestsByMethod  sync.Map // map[string]*rpcMethodCounters
	waitTimeByScope   sync.Map // map[string]*rpcWaitCounters
	labelMu           sync.Mutex
	methodLabels      int
	waitLabels        int
	overflowMethod    rpcMethodCounters
	overflowWait      rpcWaitCounters
	unscopedWaitNanos atomic.Int64

	floodWaitCount    atomic.Int64
	floodWaitDeferred atomic.Int64
	floodWaitNanos    atomic.Int64
}

// NewInMemoryRPCMetrics initializes an in-memory RPC metrics tracker.
func NewInMemoryRPCMetrics() *InMemoryRPCMetrics {
	return &InMemoryRPCMetrics{}
}

func (m *InMemoryRPCMetrics) methodCounters(method string) *rpcMethodCounters {
	if m == nil || method == "" {
		return nil
	}
	if value, ok := m.requestsByMethod.Load(method); ok {
		return value.(*rpcMethodCounters)
	}
	m.labelMu.Lock()
	defer m.labelMu.Unlock()
	if value, ok := m.requestsByMethod.Load(method); ok {
		return value.(*rpcMethodCounters)
	}
	if m.methodLabels >= maxRPCMethodMetricLabels {
		return &m.overflowMethod
	}
	created := &rpcMethodCounters{}
	m.requestsByMethod.Store(method, created)
	m.methodLabels++
	return created
}

func (m *InMemoryRPCMetrics) waitCounters(scope string) *rpcWaitCounters {
	if m == nil || scope == "" {
		return nil
	}
	if value, ok := m.waitTimeByScope.Load(scope); ok {
		return value.(*rpcWaitCounters)
	}
	m.labelMu.Lock()
	defer m.labelMu.Unlock()
	if value, ok := m.waitTimeByScope.Load(scope); ok {
		return value.(*rpcWaitCounters)
	}
	if m.waitLabels >= maxRPCWaitMetricLabels {
		return &m.overflowWait
	}
	created := &rpcWaitCounters{}
	m.waitTimeByScope.Store(scope, created)
	m.waitLabels++
	return created
}

func (m *InMemoryRPCMetrics) ObserveRequest(method string, class RPCErrorClass, attempt int, elapsed time.Duration) {
	if m == nil {
		return
	}
	classIndex := int(class)
	if classIndex < 0 || classIndex >= len(m.requestsByClass) {
		classIndex = int(RPCUnknown)
	}
	m.requestsByClass[classIndex].Add(1)
	if counters := m.methodCounters(method); counters != nil {
		counters.requests.Add(1)
	}
}

func (m *InMemoryRPCMetrics) ObserveWait(scope string, elapsed time.Duration) {
	if m == nil {
		return
	}
	if counters := m.waitCounters(scope); counters != nil {
		counters.nanos.Add(int64(elapsed))
		return
	}
	m.unscopedWaitNanos.Add(int64(elapsed))
}

func (m *InMemoryRPCMetrics) ObserveFloodWait(method string, retryAfter time.Duration, deferred bool) {
	if m == nil {
		return
	}
	m.floodWaitCount.Add(1)
	if deferred {
		m.floodWaitDeferred.Add(1)
	}
	m.floodWaitNanos.Add(int64(retryAfter))
}

// Snapshot returns an immutable copy of current metrics. Concurrent observations
// may race with the snapshot in the ordinary metrics sense, so fields are not a
// transactional point-in-time view; each individual counter remains atomic.
func (m *InMemoryRPCMetrics) Snapshot() RPCMetricsSnapshot {
	if m == nil {
		return RPCMetricsSnapshot{
			RequestsByClass:  make(map[RPCErrorClass]int64),
			RequestsByMethod: make(map[string]int64),
			WaitTimeByScope:  make(map[string]time.Duration),
		}
	}

	classes := make(map[RPCErrorClass]int64, len(m.requestsByClass))
	var totalRequests int64
	for classIndex := range m.requestsByClass {
		count := m.requestsByClass[classIndex].Load()
		if count != 0 {
			classes[RPCErrorClass(classIndex)] = count
		}
		totalRequests += count
	}

	methods := make(map[string]int64)
	m.requestsByMethod.Range(func(key, value any) bool {
		count := value.(*rpcMethodCounters).requests.Load()
		if count != 0 {
			methods[key.(string)] = count
		}
		return true
	})
	if overflow := m.overflowMethod.requests.Load(); overflow != 0 {
		methods[rpcMetricOverflowLabel] = overflow
	}

	waits := make(map[string]time.Duration)
	totalWaitNanos := m.unscopedWaitNanos.Load()
	m.waitTimeByScope.Range(func(key, value any) bool {
		nanos := value.(*rpcWaitCounters).nanos.Load()
		if nanos != 0 {
			waits[key.(string)] = time.Duration(nanos)
		}
		totalWaitNanos += nanos
		return true
	})
	if overflow := m.overflowWait.nanos.Load(); overflow != 0 {
		waits[rpcMetricOverflowLabel] = time.Duration(overflow)
		totalWaitNanos += overflow
	}

	return RPCMetricsSnapshot{
		TotalRequests:     totalRequests,
		RequestsByClass:   classes,
		RequestsByMethod:  methods,
		TotalWaitTime:     time.Duration(totalWaitNanos),
		WaitTimeByScope:   waits,
		FloodWaitCount:    m.floodWaitCount.Load(),
		FloodWaitDeferred: m.floodWaitDeferred.Load(),
		FloodWaitTotal:    time.Duration(m.floodWaitNanos.Load()),
	}
}
