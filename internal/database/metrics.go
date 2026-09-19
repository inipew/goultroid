package database

import (
	"sync"
	"sync/atomic"
	"time"
)

// DBMetrics defines an interface for observing database query and mutation latencies.
// Labels must be bounded identifiers (e.g. "peer.find", "peer.save", "settings.resolve").
type DBMetrics interface {
	Observe(operation string, elapsed time.Duration, err error)
}

// NoopDBMetrics discards all database metric observations.
type NoopDBMetrics struct{}

func (NoopDBMetrics) Observe(string, time.Duration, error) {}

type dbMetricCounters struct {
	count         atomic.Int64
	durationNanos atomic.Int64
	errors        atomic.Int64
}

// InMemoryDBMetrics records database operation counts and latencies for
// diagnostics and testing. Labels are registered lazily in sync.Map; once a
// bounded label is warm, Observe only performs a map load and atomic increments
// instead of contending on one collector-wide mutex.
type InMemoryDBMetrics struct {
	operations      sync.Map // map[string]*dbMetricCounters
	totalOperations atomic.Int64
}

// NewInMemoryDBMetrics creates an initialized in-memory database metrics collector.
func NewInMemoryDBMetrics() *InMemoryDBMetrics {
	return &InMemoryDBMetrics{}
}

func (m *InMemoryDBMetrics) counters(operation string) *dbMetricCounters {
	if m == nil {
		return nil
	}
	if value, ok := m.operations.Load(operation); ok {
		return value.(*dbMetricCounters)
	}
	created := &dbMetricCounters{}
	actual, _ := m.operations.LoadOrStore(operation, created)
	return actual.(*dbMetricCounters)
}

func (m *InMemoryDBMetrics) Observe(operation string, elapsed time.Duration, err error) {
	counters := m.counters(operation)
	if counters == nil {
		return
	}
	counters.count.Add(1)
	counters.durationNanos.Add(int64(elapsed))
	if err != nil {
		counters.errors.Add(1)
	}
	m.totalOperations.Add(1)
}

// Count returns the number of times an operation was invoked.
func (m *InMemoryDBMetrics) Count(operation string) int64 {
	if m == nil {
		return 0
	}
	value, ok := m.operations.Load(operation)
	if !ok {
		return 0
	}
	return value.(*dbMetricCounters).count.Load()
}

// Errors returns the number of errors observed for an operation.
func (m *InMemoryDBMetrics) Errors(operation string) int64 {
	if m == nil {
		return 0
	}
	value, ok := m.operations.Load(operation)
	if !ok {
		return 0
	}
	return value.(*dbMetricCounters).errors.Load()
}

// TotalDuration returns cumulative latency observed for one bounded operation label.
func (m *InMemoryDBMetrics) TotalDuration(operation string) time.Duration {
	if m == nil {
		return 0
	}
	value, ok := m.operations.Load(operation)
	if !ok {
		return 0
	}
	return time.Duration(value.(*dbMetricCounters).durationNanos.Load())
}

// TotalOperations returns the total operations across all labels.
func (m *InMemoryDBMetrics) TotalOperations() int64 {
	if m == nil {
		return 0
	}
	return m.totalOperations.Load()
}
