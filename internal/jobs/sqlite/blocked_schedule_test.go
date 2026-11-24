package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestForbidOverlapKeepsBlockedOccurrenceAsActive(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	if err := store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "external-job", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "external", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-2 * time.Minute)
	if err := store.SaveSchedule(ctx, &jobs.JobSchedule{ID: "external-schedule", JobID: "external-job", Recurrence: "interval", Interval: time.Minute, NextDueAt: due, Timezone: "UTC", MisfirePolicy: jobs.MisfireRunOnce, OverlapPolicy: jobs.OverlapForbid, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	first, err := store.MaterializeDueSchedule(ctx, "external-schedule", due.Add(time.Minute))
	if err != nil || first == nil {
		t.Fatalf("first occurrence = %+v, error = %v", first, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE job_occurrences SET state = 'blocked' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	active, err := store.ListActiveScheduleOccurrences(ctx, "external-schedule")
	if err != nil || len(active) != 0 {
		t.Fatalf("schedule cancellation must leave blocked work for review: ids=%v error=%v", active, err)
	}
	if err := store.CancelOccurrence(ctx, first.ID, "schedule deleted"); err == nil {
		t.Fatal("ordinary cancellation erased unknown-effect quarantine")
	}
	second, err := store.MaterializeDueSchedule(ctx, "external-schedule", time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("blocked effect allowed overlapping occurrence: %+v", second)
	}
}

func TestBlockExpiredAttemptRequiresUncommittedLatestLease(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()
	if err := store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "fenced-job", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "external", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	occ := &jobs.JobOccurrence{ID: "fenced-occ", JobID: "fenced-job", OccurrenceKey: "fenced-key", State: jobs.OccurrenceReady, ReadyAt: time.Now().UTC()}
	if err := store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.PrepareNextAttemptLease(ctx, occ.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if blocked, err := store.BlockExpiredAttempt(ctx, occ.ID, attempt.ID, attempt.LeaseEpoch, time.Now().UTC()); err != nil || blocked {
		t.Fatalf("live lease blocked=%t error=%v", blocked, err)
	}
	if err := store.CommitAttemptResult(ctx, attempt.ID, attempt.LeaseEpoch, jobs.AttemptCompleted, nil, ""); err != nil {
		t.Fatal(err)
	}
	if blocked, err := store.BlockExpiredAttempt(ctx, occ.ID, attempt.ID, attempt.LeaseEpoch, attempt.LeaseUntil.Add(time.Second)); err != nil || blocked {
		t.Fatalf("committed attempt blocked=%t error=%v", blocked, err)
	}
	current, err := store.GetOccurrence(ctx, occ.ID)
	if err != nil || current.State != jobs.OccurrenceCompleted {
		t.Fatalf("completed occurrence changed: %+v error=%v", current, err)
	}
}
