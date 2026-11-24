package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestCancelRedesignedScheduleCancelsActiveOccurrence(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	if _, err := h.jobDB.ExecContext(ctx, `UPDATE execution_runtime_state SET mode = 'redesigned' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	if err := h.jobs.RegisterHandler("cancel.active", func(runCtx context.Context, _ jobs.JobDefinition) error {
		close(started)
		select {
		case <-runCtx.Done():
			close(cancelled)
			return runCtx.Err()
		case <-release:
			return nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.Register(jobs.JobDefinition{
		ID: "cancel-active-target", ScopeOwner: "cancel-active-target", QuotaOwner: "cancel-active-target",
		HandlerType: "cancel.active", Pool: "general", Class: string(tasks.PriorityNormal),
		Timeout: 10 * time.Second, RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := h.sched.ScheduleManagedJob(ctx, "cancel-active-target", time.Now().UTC().Add(-time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("managed occurrence did not start")
	}
	var occurrenceID string
	if err := h.jobDB.QueryRowContext(ctx, `SELECT id FROM job_occurrences WHERE schedule_id = ?`, redesignedScheduleID(job.ID)).Scan(&occurrenceID); err != nil {
		t.Fatal(err)
	}
	if err := h.sched.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("active redesigned occurrence kept running after schedule cancellation")
	}
	occurrence, err := h.jobs.GetOccurrence(ctx, occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if occurrence.State != jobs.OccurrenceCancelled {
		t.Fatalf("occurrence state = %s, want cancelled", occurrence.State)
	}
}

func TestCancelRedesignedScheduleRemovesQueuedOccurrence(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	if _, err := h.jobDB.ExecContext(ctx, `UPDATE execution_runtime_state SET mode = 'redesigned' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	started := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		_, err := h.engine.Submit(ctx, tasks.WorkSpec{
			ID:         tasks.TaskID(fmt.Sprintf("scheduler-cancel-blocker-%d", i)),
			QuotaOwner: tasks.OwnerID(fmt.Sprintf("scheduler-cancel-blocker-%d", i)),
			Pool:       "general", Class: tasks.PriorityNormal,
			Handler: func(runCtx context.Context) error {
				started <- struct{}{}
				select {
				case <-release:
					return nil
				case <-runCtx.Done():
					return runCtx.Err()
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("general pool blockers did not start")
		}
	}
	var calls atomic.Int32
	if err := h.jobs.RegisterHandler("cancel.queued", func(context.Context, jobs.JobDefinition) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.Register(jobs.JobDefinition{
		ID: "cancel-queued-target", ScopeOwner: "cancel-queued-target", QuotaOwner: "cancel-queued-target",
		HandlerType: "cancel.queued", Pool: "general", Class: string(tasks.PriorityNormal),
		Timeout: 10 * time.Second, RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := h.sched.ScheduleManagedJob(ctx, "cancel-queued-target", time.Now().UTC().Add(-time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	var occurrenceID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = h.jobDB.QueryRowContext(ctx, `SELECT id FROM job_occurrences WHERE schedule_id = ?`, redesignedScheduleID(job.ID)).Scan(&occurrenceID)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if occurrenceID == "" {
		t.Fatalf("queued occurrence was not materialized: %v", err)
	}
	if err := h.sched.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	occurrence, err := h.jobs.GetOccurrence(ctx, occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if occurrence.State != jobs.OccurrenceCancelled {
		t.Fatalf("queued occurrence state = %s, want cancelled", occurrence.State)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("queued handler calls = %d, want none", got)
	}
}

func TestCancelLegacyScheduleFindsOccurrenceAfterClaimTrackingIsLost(t *testing.T) {
	h := newTimingHarness(t)
	ctx := context.Background()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	if err := h.jobs.RegisterHandler("cancel.legacy", func(runCtx context.Context, _ jobs.JobDefinition) error {
		close(started)
		select {
		case <-runCtx.Done():
			close(cancelled)
			return runCtx.Err()
		case <-release:
			return nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.Register(jobs.JobDefinition{
		ID: "cancel-legacy-target", ScopeOwner: "cancel-legacy-target", QuotaOwner: "cancel-legacy-target",
		HandlerType: "cancel.legacy", Pool: "general", Class: string(tasks.PriorityNormal),
		Timeout: 10 * time.Second, RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := h.sched.ScheduleManagedJob(ctx, "cancel-legacy-target", time.Now().UTC().Add(-time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy managed occurrence did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.sched.trackedClaimCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.sched.trackedClaimCount() == 0 {
		t.Fatal("legacy claim was never tracked")
	}
	h.sched.untrackClaim(job.ID) // restart loses this in-memory map
	if err := h.sched.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("legacy ActionJob kept running after cancellation without an in-memory claim")
	}
}

func TestCancelFutureScheduleWithoutOccurrence(t *testing.T) {
	h := newTimingHarness(t)
	job, err := h.sched.ScheduleOnce(context.Background(), 808, "chat", 0, time.Now().UTC().Add(time.Hour), ActionMessage, "later")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.sched.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := h.repo.ListScheduledJobs(context.Background(), 808)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("future schedule still listed after cancellation: %+v", rows)
	}
}
