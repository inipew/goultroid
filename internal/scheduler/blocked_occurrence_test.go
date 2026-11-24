package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
)

func TestLegacyScheduleDoesNotRetryBlockedOccurrence(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		resolved bool
		tracked  bool
	}{
		{name: "blocked rejected submission"},
		{name: "blocked tracked reconciliation", tracked: true},
		{name: "resolved rejected submission", resolved: true},
		{name: "resolved tracked reconciliation", resolved: true, tracked: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := newTimingHarness(t)
			ctx := context.Background()
			due := time.Now().UTC().Add(-time.Minute)
			row, err := h.repo.CreateScheduledJob(ctx, &ScheduledJob{
				ChatID: 1, ActionType: ActionJob, Payload: "blocked-target",
				NextRunAt: due, Status: JobStatusRunning, ClaimToken: "held-claim",
			})
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("sched:%d:%d", row.ID, due.UnixNano())
			if err := h.store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "blocked-target", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "test", Pool: "general", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			occ := &jobs.JobOccurrence{ID: "blocked-slot", JobID: "blocked-target", OccurrenceKey: key, State: jobs.OccurrenceBlocked, ReadyAt: due}
			if err := h.store.MaterializeOccurrence(ctx, occ); err != nil {
				t.Fatal(err)
			}
			if scenario.resolved {
				if err := h.store.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceCompleted, "verified external delivery"); err != nil {
					t.Fatal(err)
				}
			}
			if !scenario.tracked {
				h.sched.onSubmitRejected(ctx, *row, key, errors.New("blocked occurrence cannot be replayed"))
			} else {
				h.sched.trackClaim(row.ID, row.ClaimToken, "blocked-target", occ.ID)
				h.sched.reconcileSettledClaims(ctx)
			}
			current, err := h.repo.GetScheduledJob(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current == nil || current.Status != JobStatusFailed {
				t.Fatalf("compatibility row = %+v, want failed quarantine", current)
			}
			if scenario.tracked && h.sched.Diagnostics().ActiveLegacyClaims != 0 {
				t.Fatal("blocked occurrence retained an active legacy claim")
			}
		})
	}
}

func TestCancelLegacySchedulePreservesBlockedOccurrence(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(time.Hour)
	row, err := h.repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID: 2, ActionType: ActionJob, Payload: "blocked-target",
		NextRunAt: due, Status: JobStatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "blocked-target", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("sched:%d:%d", row.ID, due.UnixNano())
	occ := &jobs.JobOccurrence{ID: "blocked-cancel-slot", JobID: "blocked-target", OccurrenceKey: key, State: jobs.OccurrenceBlocked, ReadyAt: due}
	if err := h.store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	if err := h.sched.Cancel(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	current, err := h.store.GetOccurrence(ctx, occ.ID)
	if err != nil || current.State != jobs.OccurrenceBlocked {
		t.Fatalf("blocked occurrence changed by schedule deletion: %+v error=%v", current, err)
	}
	deleted, err := h.repo.GetScheduledJob(ctx, row.ID)
	if err != nil || deleted != nil {
		t.Fatalf("cancelled compatibility row = %+v, error=%v", deleted, err)
	}
}

func TestMisfireSkipDoesNotAdvanceUnknownEffectLegacySlot(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		name := "blocked"
		if resolved {
			name = "manually resolved"
		}
		t.Run(name, func(t *testing.T) {
			h := newTimingHarness(t)
			ctx := context.Background()
			h.sched.SetMisfirePolicy(MisfireSkip)
			due := time.Now().UTC().Add(-2 * time.Minute)
			leaseUntil := time.Now().UTC().Add(time.Hour)
			row, err := h.repo.CreateScheduledJob(ctx, &ScheduledJob{
				ChatID: 3, ActionType: ActionJob, Payload: "blocked-target",
				IntervalSeconds: 60, NextRunAt: due, Status: JobStatusRunning,
				ClaimToken: "old-lease", LeaseUntil: &leaseUntil,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "blocked-target", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "test", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("sched:%d:%d", row.ID, due.UnixNano())
			occ := &jobs.JobOccurrence{ID: "unknown-misfire-slot", JobID: "blocked-target", OccurrenceKey: key, State: jobs.OccurrenceBlocked, ReadyAt: due}
			if err := h.store.MaterializeOccurrence(ctx, occ); err != nil {
				t.Fatal(err)
			}
			if resolved {
				if err := h.store.ResolveBlockedOccurrence(ctx, occ.ID, jobs.OccurrenceCompleted, "verified external delivery"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.repo.db.ExecContext(ctx, `UPDATE scheduled_jobs SET lease_until = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second), row.ID); err != nil {
				t.Fatal(err)
			}
			h.sched.processDueJobs(ctx, time.Now().UTC())
			current, err := h.repo.GetScheduledJob(ctx, row.ID)
			if err != nil || current == nil || current.Status != JobStatusFailed {
				t.Fatalf("unknown-effect recurring row advanced: %+v error=%v", current, err)
			}
		})
	}
}

func TestUnknownEffectReviewReadFailurePreservesLegacyDueSlot(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	due := time.Now().UTC().Add(-time.Minute)
	row, err := h.repo.CreateScheduledJob(ctx, &ScheduledJob{
		ChatID: 4, ActionType: ActionJob, Payload: "review-target",
		NextRunAt: due, Status: JobStatusRunning, ClaimToken: "review-claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveDefinition(ctx, &jobs.JobDefinition{ID: "review-target", ScopeOwner: "test", QuotaOwner: "test", HandlerType: "test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("sched:%d:%d", row.ID, due.UnixNano())
	occ := &jobs.JobOccurrence{ID: "review-read-slot", JobID: "review-target", OccurrenceKey: key, State: jobs.OccurrenceReady, ReadyAt: due}
	if err := h.store.MaterializeOccurrence(ctx, occ); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobDB.ExecContext(ctx, `DROP TABLE job_outbox`); err != nil {
		t.Fatal(err)
	}
	h.sched.onSubmitRejected(ctx, *row, key, errors.New("admission failed"))
	current, err := h.repo.GetScheduledJob(ctx, row.ID)
	if err != nil || current == nil || current.Status != JobStatusRunning || !current.NextRunAt.Equal(due) {
		t.Fatalf("ambiguous review read moved due slot: %+v error=%v", current, err)
	}
}
