package database

import (
	"sync"
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

// InMemoryDBMetrics records database operation counts and latencies for diagnostics and testing.
type InMemoryDBMetrics struct {
	mu             sync.RWMutex
	operationCount map[string]int64
	totalDuration  map[string]time.Duration
	errorCount     map[string]int64
}

// NewInMemoryDBMetrics creates an initialized in-memory database metrics collector.
func NewInMemoryDBMetrics() *InMemoryDBMetrics {
	return &InMemoryDBMetrics{
		operationCount: make(map[string]int64),
		totalDuration:  make(map[string]time.Duration),
		errorCount:     make(map[string]int64),
	}
}

func (m *InMemoryDBMetrics) Observe(operation string, elapsed time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.operationCount[operation]++
	m.totalDuration[operation] += elapsed
	if err != nil {
		m.errorCount[operation]++
	}
}

// Count returns the number of times an operation was invoked.
func (m *InMemoryDBMetrics) Count(operation string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.operationCount[operation]
}

// Errors returns the number of errors observed for an operation.
func (m *InMemoryDBMetrics) Errors(operation string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.errorCount[operation]
}

// TotalOperations returns the total operations across all labels.
func (m *InMemoryDBMetrics) TotalOperations() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total int64
	for _, c := range m.operationCount {
		total += c
	}
	return total
}
