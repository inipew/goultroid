package scheduler

import (
	"context"
	"strings"
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

func TestPeriodicTaskSnapshot_LastErrorBounded(t *testing.T) {
	coord := newPeriodicCoordinator(nil)
	longError := strings.Repeat("Periodic task execution failed due to an extremely long reason message. ", 10)
	if len(longError) <= 256 {
		t.Fatalf("test setup error: longError length %d <= 256", len(longError))
	}

	coord.entries["task1"] = &periodicRegistration{
		Owner:     "test-owner",
		Name:      "task1",
		Runs:      5,
		Failures:  1,
		LastRunAt: time.Now(),
		LastError: longError,
	}

	snaps := coord.Snapshots()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if len(snaps[0].LastError) > 256 {
		t.Fatalf("snapshot LastError length = %d, want <= 256", len(snaps[0].LastError))
	}
	if snaps[0].LastError != longError[:256] {
		t.Fatalf("snapshot LastError does not match truncated prefix: %q", snaps[0].LastError)
	}

	// Verify multi-byte UTF-8 cut and sensitive credential redaction
	coord.entries["task2"] = &periodicRegistration{
		Owner:     "test-owner",
		Name:      "task2",
		Runs:      1,
		Failures:  1,
		LastRunAt: time.Now(),
		LastError: "failed connecting to https://user:pass123@api.example.com with token=secret_val_456 " + strings.Repeat("世", 100),
	}

	snaps = coord.Snapshots()
	var task2Snap *PeriodicTaskSnapshot
	for i := range snaps {
		if snaps[i].Name == "task2" {
			task2Snap = &snaps[i]
			break
		}
	}
	if task2Snap == nil {
		t.Fatal("expected task2 snapshot")
	}
	if len(task2Snap.LastError) > 256 {
		t.Fatalf("task2 LastError length = %d > 256", len(task2Snap.LastError))
	}
	if strings.Contains(task2Snap.LastError, "pass123") || strings.Contains(task2Snap.LastError, "secret_val_456") {
		t.Fatalf("task2 LastError leaked secrets: %s", task2Snap.LastError)
	}
	if !strings.Contains(task2Snap.LastError, "[REDACTED]") {
		t.Fatalf("task2 LastError did not redact: %s", task2Snap.LastError)
	}
}
