package jobs_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/jobs"
	jobsqlite "github.com/inipew/goultroid/internal/jobs/sqlite"
)

func TestExpiredLeaseIsBlockedAfterSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "expired-lease.db")
	before, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobsqlite.InitSchema(ctx, before.DB); err != nil {
		t.Fatal(err)
	}
	store := jobsqlite.NewStore(before.DB)
	def := jobs.JobDefinition{ID: "effect-unknown", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "external", Pool: "general", RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 3}, Enabled: true}
	if err := store.SaveDefinition(ctx, &def); err != nil {
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{ID: "occ-effect-unknown", JobID: def.ID, OccurrenceKey: "sched:unknown:slot", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.PrepareNextAttemptLease(ctx, occ.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(attempt.LeaseUntil) + 5*time.Millisecond)

	after, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if err := jobsqlite.InitSchema(ctx, after.DB); err != nil {
		t.Fatal(err)
	}
	reopened := jobsqlite.NewStore(after.DB)
	manager := jobs.NewManagerWithPorts(nil, jobsqlite.Ports(reopened), nil)
	if err := manager.RegisterHandler("external", func(context.Context, jobs.JobDefinition) error {
		t.Fatal("unknown external effect was replayed")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(def); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Recover(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if report.Blocked != 1 || report.Redriven != 0 {
		t.Fatalf("recovery report = %+v, want one blocked and no redrive", report)
	}
	current, err := reopened.GetOccurrence(ctx, occ.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != jobs.OccurrenceBlocked {
		t.Fatalf("occurrence state = %s, want blocked", current.State)
	}
	if n, err := reopened.CountAttempts(ctx, occ.ID); err != nil || n != 1 {
		t.Fatalf("attempts = %d, error = %v, want one", n, err)
	}
	if err := reopened.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptCompleted, nil, ""); !errors.Is(err, jobsqlite.ErrLeaseFencingLost) {
		t.Fatalf("late completion error = %v, want lease fencing", err)
	}
	if again, err := manager.Recover(ctx, 10); err != nil || again.Redriven != 0 || again.Blocked != 0 {
		t.Fatalf("repeat recovery = %+v, error = %v", again, err)
	}
	if diagnostics := manager.Diagnostics(); !diagnostics.DurableSnapshotOK || diagnostics.BlockedOccurrences != 1 {
		t.Fatalf("blocked occurrence missing from diagnostics: %+v", diagnostics)
	}
	blocked, err := manager.ListBlockedOccurrences(ctx, 10)
	if err != nil || len(blocked) != 1 || blocked[0].ID != occ.ID {
		t.Fatalf("blocked work list = %+v, error = %v", blocked, err)
	}

	// A plugin can be absent during restart. The durable effect still needs
	// quarantine even when its handler is unavailable in the new process.
	missing := jobs.JobDefinition{ID: "missing-handler", ScopeOwner: "plugin:missing", QuotaOwner: "test", HandlerType: "missing", Pool: "general", Enabled: true}
	if err := reopened.SaveDefinition(ctx, &missing); err != nil {
		t.Fatal(err)
	}
	orphan := &jobs.JobOccurrence{ID: "occ-missing-handler", JobID: missing.ID, OccurrenceKey: "sched:missing:slot", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := reopened.MaterializeOccurrence(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	orphanAttempt, err := reopened.PrepareNextAttemptLease(ctx, orphan.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(orphanAttempt.LeaseUntil) + 5*time.Millisecond)
	report, err = manager.Recover(ctx, 10)
	if err != nil || report.Blocked != 1 || report.Orphaned != 0 {
		t.Fatalf("missing-handler recovery = %+v, error = %v", report, err)
	}
	if err := manager.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceReady, "retry it"); err == nil {
		t.Fatal("manual review reopened an unknown external effect for retry")
	}
	if err := manager.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceCompleted, " "); err == nil {
		t.Fatal("manual review accepted an empty reason")
	}
	if err := manager.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceCompleted, "operator verified Telegram delivery"); err != nil {
		t.Fatal(err)
	}
	resolved, err := reopened.GetOccurrence(ctx, occ.ID)
	if err != nil || resolved.State != jobs.OccurrenceCompleted {
		t.Fatalf("manual resolution = %+v, error = %v", resolved, err)
	}
	if err := manager.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceCompleted, "operator verified Telegram delivery"); err != nil {
		t.Fatalf("idempotent resolution: %v", err)
	}
	if err := manager.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceFailed, "conflicting review"); err == nil {
		t.Fatal("conflicting manual resolution was accepted")
	}
	var eventPayload string
	if err := after.DB.QueryRowContext(ctx, `SELECT payload FROM job_outbox WHERE event_id = ?`, "outbox:resolve:"+occ.ID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(eventPayload, "operator verified Telegram delivery") {
		t.Fatalf("manual review reason not durable: %q", eventPayload)
	}
	normal := &jobs.JobOccurrence{ID: "ordinary-completed", JobID: def.ID, OccurrenceKey: "manual:ordinary-completed", State: jobs.OccurrenceCompleted, ReadyAt: time.Now().UTC()}
	if err := reopened.MaterializeOccurrence(ctx, normal); err != nil {
		t.Fatal(err)
	}
	if err := manager.ResolveBlockedOccurrence(ctx, normal.ID, jobs.OccurrenceCompleted, "not a blocked occurrence"); err == nil {
		t.Fatal("ordinary completed occurrence accepted a blocked-work resolution")
	}
}
