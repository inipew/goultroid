package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestReconciliationDiagnosticsCountLegacyReads(t *testing.T) {
	repo := &executionReconcileMeasurementRepository{rows: map[int64]*ScheduledJob{
		7: {ID: 7, Status: JobStatusRunning, ClaimToken: "claim-7"},
	}}
	occurrences := &executionReconcileMeasurementOccurrenceStore{
		executionReconcileOccurrenceStore: &executionReconcileOccurrenceStore{},
	}
	manager := jobs.NewManagerWithPorts(nil, jobs.StorePorts{Occurrences: occurrences}, nil)
	engine := NewEngine(repo, nil)
	engine.SetJobsManager(manager)
	engine.trackClaim(7, "claim-7", "definition-7", "occ-7")

	engine.reconcileSettledClaims(context.Background())
	engine.reconcileSettledClaims(context.Background())
	stats := engine.Diagnostics()
	if stats.ActiveLegacyClaims != 1 || stats.ReconcilePasses != 2 || stats.CompletedReconcilePasses != 2 || stats.ClaimsInspected != 2 {
		t.Fatalf("reconciliation counters = %+v, want 1 active claim and 2 inspections", stats)
	}
	if stats.ScheduledReads != 2 || stats.OccurrenceReads != 2 {
		t.Fatalf("repository read counters = %+v, want 2 reads from each store", stats)
	}
	if stats.ReconcileDuration <= 0 {
		t.Fatalf("reconciliation duration = %s, want positive", stats.ReconcileDuration)
	}
}

func TestSchedulerDiagnosticsAttributeNotificationWake(t *testing.T) {
	h := newTimingHarness(t)
	before := h.sched.Diagnostics()
	h.sched.notifyWake()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		after := h.sched.Diagnostics()
		if after.NotificationWakes > before.NotificationWakes && after.ReconcilePasses > before.ReconcilePasses {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scheduler did not attribute notification wake: before=%+v after=%+v", before, h.sched.Diagnostics())
}
