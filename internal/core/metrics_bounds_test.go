package core

import (
	"fmt"
	"testing"
	"time"
)

func TestCommandMetricsLabelCardinalityIsBounded(t *testing.T) {
	m := NewDefaultMetricsTracker()
	for i := 0; i < maxCommandMetricLabels+100; i++ {
		m.RecordCommand(fmt.Sprintf("plugin-command-%d", i), time.Microsecond, nil)
	}
	snap := m.Snapshot()
	if len(snap.Commands) != maxCommandMetricLabels {
		t.Fatalf("command metric labels=%d, want %d", len(snap.Commands), maxCommandMetricLabels)
	}
	overflow := snap.Commands[metricsOverflowLabel]
	if overflow == nil || overflow.TotalCalls != 101 {
		got := int64(0)
		if overflow != nil {
			got = overflow.TotalCalls
		}
		t.Fatalf("overflow calls=%d, want 101", got)
	}
}
