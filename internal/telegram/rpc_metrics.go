package telegram

import (
	"sync"
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

// InMemoryRPCMetrics tracks RPC metrics in memory with concurrency safety.
type InMemoryRPCMetrics struct {
	mu                sync.RWMutex
	totalRequests     int64
	requestsByClass   map[RPCErrorClass]int64
	requestsByMethod  map[string]int64
	totalWaitTime     time.Duration
	waitTimeByScope   map[string]time.Duration
	floodWaitCount    int64
	floodWaitDeferred int64
	floodWaitTotal    time.Duration
}

// NewInMemoryRPCMetrics initializes an in-memory metrics tracker.
func NewInMemoryRPCMetrics() *InMemoryRPCMetrics {
	return &InMemoryRPCMetrics{
		requestsByClass:  make(map[RPCErrorClass]int64),
		requestsByMethod: make(map[string]int64),
		waitTimeByScope:  make(map[string]time.Duration),
	}
}

func (m *InMemoryRPCMetrics) ObserveRequest(method string, class RPCErrorClass, attempt int, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totalRequests++
	m.requestsByClass[class]++
	if method != "" {
		m.requestsByMethod[method]++
	}
}

func (m *InMemoryRPCMetrics) ObserveWait(scope string, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.totalWaitTime += elapsed
	if scope != "" {
		m.waitTimeByScope[scope] += elapsed
	}
}

func (m *InMemoryRPCMetrics) ObserveFloodWait(method string, retryAfter time.Duration, deferred bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.floodWaitCount++
	if deferred {
		m.floodWaitDeferred++
	}
	m.floodWaitTotal += retryAfter
}

// Snapshot returns an immutable copy of current metrics.
func (m *InMemoryRPCMetrics) Snapshot() RPCMetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	classes := make(map[RPCErrorClass]int64, len(m.requestsByClass))
	for k, v := range m.requestsByClass {
		classes[k] = v
	}
	methods := make(map[string]int64, len(m.requestsByMethod))
	for k, v := range m.requestsByMethod {
		methods[k] = v
	}
	waits := make(map[string]time.Duration, len(m.waitTimeByScope))
	for k, v := range m.waitTimeByScope {
		waits[k] = v
	}
	return RPCMetricsSnapshot{
		TotalRequests:     m.totalRequests,
		RequestsByClass:   classes,
		RequestsByMethod:  methods,
		TotalWaitTime:     m.totalWaitTime,
		WaitTimeByScope:   waits,
		FloodWaitCount:    m.floodWaitCount,
		FloodWaitDeferred: m.floodWaitDeferred,
		FloodWaitTotal:    m.floodWaitTotal,
	}
}
