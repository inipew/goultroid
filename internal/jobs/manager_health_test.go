package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/runtime"
)

func TestJobManager_HealthAcceptingAndClosed(t *testing.T) {
	m := &Manager{accepting: true}
	h := m.Health(context.Background())
	if h.Status != runtime.HealthHealthy {
		t.Fatalf("expected healthy, got %+v", h)
	}

	m.accepting = false
	h = m.Health(context.Background())
	if h.Status != runtime.HealthDegraded || h.Details != "admission-closed" {
		t.Fatalf("expected degraded admission-closed, got %+v", h)
	}
}

func TestJobManager_HealthRecoveryFailing(t *testing.T) {
	m := &Manager{accepting: true}
	now := time.Now().UTC()
	m.recoveryDiagnostics = RecoveryDiagnostics{
		FailureCount:  1,
		LastFailureAt: now,
		LastSuccessAt: now.Add(-time.Minute),
	}

	h := m.Health(context.Background())
	if h.Status != runtime.HealthDegraded || h.Details != "recovery-failing" {
		t.Fatalf("expected degraded recovery-failing, got %+v", h)
	}

	// When superseded by success
	m.recoveryDiagnostics.LastSuccessAt = now.Add(time.Second)
	h = m.Health(context.Background())
	if h.Status != runtime.HealthHealthy {
		t.Fatalf("expected healthy after recovery success, got %+v", h)
	}
}

func TestJobManager_HealthContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := &Manager{accepting: true, baseCtx: ctx}
	h := m.Health(context.Background())
	if h.Status != runtime.HealthDegraded || h.Details != "context-cancelled" {
		t.Fatalf("expected degraded context-cancelled, got %+v", h)
	}
}
