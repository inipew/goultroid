package scheduler

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/runtime"
)

func TestSchedulerEngine_HealthRunningAndStopped(t *testing.T) {
	e := &Engine{running: true}
	h := e.Health(context.Background())
	if h.Status != runtime.HealthHealthy {
		t.Fatalf("expected healthy, got %+v", h)
	}

	e.running = false
	h = e.Health(context.Background())
	if h.Status != runtime.HealthDegraded || h.Details != "not-running" {
		t.Fatalf("expected degraded not-running, got %+v", h)
	}
}
