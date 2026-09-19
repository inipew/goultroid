package database

import (
	"fmt"
	"testing"
	"time"
)

func TestDBMetricsLabelCardinalityIsBounded(t *testing.T) {
	m := NewInMemoryDBMetrics()
	const extra = 100
	for i := 0; i < maxDBMetricLabels+extra; i++ {
		m.Observe(fmt.Sprintf("dynamic.%d", i), time.Microsecond, nil)
	}
	labels := 0
	m.operations.Range(func(_, _ any) bool {
		labels++
		return true
	})
	if labels != maxDBMetricLabels {
		t.Fatalf("registered labels=%d, want %d", labels, maxDBMetricLabels)
	}
	if got := m.Count(dbMetricOverflowLabel); got != extra {
		t.Fatalf("overflow count=%d, want %d", got, extra)
	}
	if got := m.TotalOperations(); got != maxDBMetricLabels+extra {
		t.Fatalf("total operations=%d", got)
	}
}
