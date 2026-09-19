package telegram

import (
	"fmt"
	"testing"
	"time"
)

func TestRPCMetricsLabelCardinalityIsBounded(t *testing.T) {
	m := NewInMemoryRPCMetrics()
	for i := 0; i < maxRPCMethodMetricLabels+100; i++ {
		m.ObserveRequest(fmt.Sprintf("dynamic.method.%d", i), RPCSuccess, 1, time.Microsecond)
	}
	for i := 0; i < maxRPCWaitMetricLabels+20; i++ {
		m.ObserveWait(fmt.Sprintf("dynamic.scope.%d", i), time.Microsecond)
	}
	snap := m.Snapshot()
	if len(snap.RequestsByMethod) != maxRPCMethodMetricLabels+1 {
		t.Fatalf("method snapshot labels=%d, want %d including overflow", len(snap.RequestsByMethod), maxRPCMethodMetricLabels+1)
	}
	if got := snap.RequestsByMethod[rpcMetricOverflowLabel]; got != 100 {
		t.Fatalf("method overflow=%d, want 100", got)
	}
	if len(snap.WaitTimeByScope) != maxRPCWaitMetricLabels+1 {
		t.Fatalf("wait snapshot labels=%d, want %d including overflow", len(snap.WaitTimeByScope), maxRPCWaitMetricLabels+1)
	}
	if got := snap.WaitTimeByScope[rpcMetricOverflowLabel]; got != 20*time.Microsecond {
		t.Fatalf("wait overflow=%s, want %s", got, 20*time.Microsecond)
	}
}
