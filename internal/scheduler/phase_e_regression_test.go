package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
)

// E/P0: admission backoff may move NextRun as a wake deadline, but the logical
// scheduled slot used for the occurrence idempotency key must remain stable.
func TestPeriodicAdmissionBackoffPreservesScheduledSlot(t *testing.T) {
	c := newPeriodicCoordinator(nil)
	base := time.Date(2026, 9, 15, 3, 0, 0, 123, time.UTC)
	c.nowFn = func() time.Time { return base.Add(time.Second) }
	key := periodicKey("runtime", "stable")
	reg := &periodicRegistration{
		Owner: "runtime", Name: "stable", Interval: time.Minute,
		ScheduledFor: base, NextRun: base, Generation: 7, Running: true,
	}
	c.entries[key] = reg
	run := periodicDueRun{owner: "runtime", name: "stable", generation: 7, interval: time.Minute, scheduledFor: base}

	c.finishSubmission(run)
	if !reg.ScheduledFor.Equal(base) {
		t.Fatalf("logical slot changed from %s to %s", base, reg.ScheduledFor)
	}
	if !reg.NextRun.After(base) {
		t.Fatalf("expected backoff wake after slot, got %s", reg.NextRun)
	}
	if reg.heapEntry == nil {
		t.Fatal("backoff did not reschedule the registration immediately")
	}
	if reg.heapEntry.Data != nil {
		t.Fatal("timer heap retained mutable registration data")
	}

	c.mu.Lock()
	due := c.collectDueLocked(reg.NextRun.Add(time.Millisecond))
	c.mu.Unlock()
	if len(due) != 1 {
		t.Fatalf("due runs=%d, want 1", len(due))
	}
	if !due[0].scheduledFor.Equal(base) {
		t.Fatalf("retry minted a new slot %s, want %s", due[0].scheduledFor, base)
	}
}

func TestPeriodicBareUnregisterCannotSelectAnotherOwner(t *testing.T) {
	c := newPeriodicCoordinator(nil)
	pluginKey := periodicKey("plugin:alpha", "same-name")
	c.entries[pluginKey] = &periodicRegistration{Owner: "plugin:alpha", Name: "same-name", Generation: 1}

	if err := c.Unregister("same-name"); err == nil {
		t.Fatal("bare runtime unregister unexpectedly removed another owner's task")
	}
	if _, ok := c.entries[pluginKey]; !ok {
		t.Fatal("plugin-owned registration was removed by bare-name unregister")
	}
}

// E/P1: managed schedules submit the target JobOccurrence directly. Scheduler
// row settlement must therefore reflect the target's terminal failure instead
// of declaring wrapper success merely because TryTrigger admitted the target.
func TestScheduledManagedJobTracksTargetOccurrenceFailure(t *testing.T) {
	h := newTimingHarness(t)
	var calls atomic.Int32
	const targetID = "managed-target-fails"
	if err := h.jobs.RegisterHandler("managed.fail", func(context.Context, jobs.JobDefinition) error {
		calls.Add(1)
		return errors.New("managed target failed")
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.jobs.Register(jobs.JobDefinition{
		ID: targetID, ScopeOwner: "managed:test", QuotaOwner: "managed:test",
		HandlerType: "managed.fail", Pool: "general", Class: string(tasks.PriorityNormal),
		RetryPolicy: jobs.JobRetryPolicy{MaxAttempts: 1}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := h.sched.ScheduleManagedJob(context.Background(), targetID, time.Now().UTC().Add(-time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := h.jobs.Definition(scheduledDefinitionID(job.ID)); exists {
		t.Fatal("managed schedule created redundant scheduler.action wrapper definition")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, listErr := h.repo.ListScheduledJobs(context.Background(), 0)
		if listErr == nil && len(rows) == 1 && rows[0].Status == JobStatusFailed {
			if calls.Load() != 1 {
				t.Fatalf("managed target calls=%d, want exactly 1", calls.Load())
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := h.repo.ListScheduledJobs(context.Background(), 0)
	t.Fatalf("managed schedule did not reflect target failure: rows=%+v calls=%d", rows, calls.Load())
}
