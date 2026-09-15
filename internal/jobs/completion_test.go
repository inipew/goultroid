package jobs

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestJobPanicReportsTerminalResult(t *testing.T) {
	m := NewManager(asyncTestSubmitter{})
	if err := m.Register(Job{ID: "panic", Owner: "owner", Run: func(context.Context) error { panic("boom") }}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := m.TriggerAndWait(ctx, "panic")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic completion = %v", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.jobs["panic"].State != StateFailed {
		t.Fatalf("job state = %s", m.jobs["panic"].State)
	}
}
